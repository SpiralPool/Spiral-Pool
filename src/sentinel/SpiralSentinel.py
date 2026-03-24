#!/usr/bin/env python3
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""
╔═════════════════════════════════════════════════════════════════════════════╗
║  Spiral Sentinel v1.2.1 - CONVERGENT SPIRAL EDITION                                 ║
║  Autonomous SHA-256 Solo Mining Monitor (DGB/BTC/BCH/BC2)                   ║
║  Self-Healing + Share Monitoring (No Pool Software Dependency)              ║
╠═════════════════════════════════════════════════════════════════════════════╣
║  FEATURES:                                                                  ║
║  • Self-healing miner management with auto-restart                          ║
║  • Zombie miner detection (miners not submitting valid shares)              ║
║  • Temperature monitoring with warning/critical alerts                      ║
║  • Power blip detection and fleet-wide power event alerts                   ║
║  • Discord webhook notifications for all events                             ║
║  • 6-hour, weekly, and monthly reports                                      ║
║  • Block found celebrations with reward tracking                            ║
╠═════════════════════════════════════════════════════════════════════════════╣
║  SUPPORTED HARDWARE:                                                        ║
║  • BitAxe, NerdQAxe++, AxeOS-based miners (HTTP API)                        ║
║  • Avalon ASICs (CGMiner API)                                               ║
║  • Bitmain Antminer S19/S21/T21 series (CGMiner API port 4028)              ║
║  • MicroBT Whatsminer M30/M50/M60 series (CGMiner API port 4028)            ║
║  • Innosilicon A10/A11/T3 series (CGMiner API port 4028)                    ║
╠═════════════════════════════════════════════════════════════════════════════╣
║  ASIC API Protocol References (API protocol, not derived code):             ║
║  • CGMiner API: github.com/ckolivas/cgminer (API-README)                    ║
║  • Whatsminer API: whatsminer.com                                           ║
╚═════════════════════════════════════════════════════════════════════════════╝
"""
__version__ = "1.2.1-CONVERGENT_SPIRAL"
__codename__ = "CONVERGENT_SPIRAL"

import copy, json, socket, sys, time, os, urllib.request, urllib.error, ssl, random, ipaddress, re, threading, http.server
from urllib.parse import urlparse, quote as url_quote
import logging

# HTTP session library (required for Braiins, Vnish, ePIC miner support)
try:
    import requests
    REQUESTS_AVAILABLE = True
except ImportError:
    requests = None
    REQUESTS_AVAILABLE = False
import signal
from logging.handlers import RotatingFileHandler
from datetime import datetime, timezone, timedelta
from pathlib import Path
try:
    from zoneinfo import ZoneInfo  # Python 3.9+
except ImportError:
    ZoneInfo = None  # Fallback to UTC if zoneinfo not available

# XMPP support (optional) — requires slixmpp (pip install slixmpp)
# NOTE: slixmpp is licensed under GPL-3.0. When installed and XMPP notifications
# are enabled, GPL-3.0 runtime obligations may apply to this Sentinel process.
# See THIRD_PARTY_LICENSES.txt for details.
try:
    import slixmpp
    import asyncio
    XMPP_AVAILABLE = True
except ImportError:
    XMPP_AVAILABLE = False

# ═══════════════════════════════════════════════════════════════════════════════
# INSTALLATION PATHS - Uses environment variable (set by systemd) with fallback
# ═══════════════════════════════════════════════════════════════════════════════
INSTALL_DIR = Path(os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool"))

# ═══════════════════════════════════════════════════════════════════════════════
# LOGGING CONFIGURATION - Structured logging with rotation
# ═══════════════════════════════════════════════════════════════════════════════
def sanitize_log_input(text):
    """
    SECURITY: Sanitize input for logging to prevent log injection attacks.
    Removes/escapes newlines, carriage returns, and control characters
    that could forge log entries or corrupt log parsing.
    """
    if not isinstance(text, str):
        text = str(text)
    # Replace newlines and carriage returns with escaped versions
    text = text.replace('\r\n', '\\r\\n')
    text = text.replace('\n', '\\n')
    text = text.replace('\r', '\\r')
    # Remove other control characters (ASCII 0-31 except tab)
    text = ''.join(c if c == '\t' or (ord(c) >= 32 and ord(c) < 127) or ord(c) >= 128 else f'\\x{ord(c):02x}' for c in text)
    return text


class SecureRotatingFileHandler(RotatingFileHandler):
    """
    SECURITY: RotatingFileHandler with secure file permissions.
    Creates log files with mode 0600 (owner read/write only).
    """
    def _open(self):
        # First, create the file with secure permissions
        stream = super()._open()
        try:
            # Set file permissions to 0600 (owner read/write only)
            os.chmod(self.baseFilename, 0o600)
        except (OSError, AttributeError):
            pass  # Best effort - may fail on Windows
        return stream

    def doRollover(self):
        """Override to set secure permissions on rotated files."""
        super().doRollover()
        # Set permissions on all rotated files
        for i in range(1, self.backupCount + 1):
            rotated_file = f"{self.baseFilename}.{i}"
            if os.path.exists(rotated_file):
                try:
                    os.chmod(rotated_file, 0o600)
                except (OSError, AttributeError):
                    pass


def setup_logging():
    """Configure structured logging with file rotation and console output."""
    log_dir = INSTALL_DIR / "logs"
    try:
        log_dir.mkdir(parents=True, exist_ok=True)
    except (PermissionError, OSError):
        pass  # File handler will fall back to console-only below
    log_file = log_dir / "sentinel.log"

    # Create formatter with timestamp, level, and message
    formatter = logging.Formatter(
        "%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S"
    )

    # Root logger configuration
    logger = logging.getLogger("SpiralSentinel")
    logger.setLevel(logging.DEBUG)

    # Clear any existing handlers
    logger.handlers.clear()

    # Console handler (INFO and above)
    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setLevel(logging.INFO)
    console_handler.setFormatter(formatter)
    logger.addHandler(console_handler)

    # SECURITY: File handler with rotation and secure permissions (10MB max, keep 5 backups)
    try:
        file_handler = SecureRotatingFileHandler(
            log_file,
            maxBytes=10*1024*1024,  # 10MB
            backupCount=5,
            encoding='utf-8'
        )
        file_handler.setLevel(logging.DEBUG)
        file_handler.setFormatter(formatter)
        logger.addHandler(file_handler)
        # Set initial file permissions
        if os.path.exists(log_file):
            try:
                os.chmod(log_file, 0o600)
            except (OSError, AttributeError):
                pass
    except (PermissionError, OSError):
        # Fall back to console-only if log directory not writable
        logger.warning("Could not create log file, using console only")

    return logger

# Initialize logger
logger = setup_logging()

_SEP = "=" * 50

# ═══════════════════════════════════════════════════════════════════════════════
# TIMEZONE HELPER - All user-facing times use configured timezone
# ═══════════════════════════════════════════════════════════════════════════════
def get_display_tz():
    """Get the configured display timezone, defaulting to America/New_York."""
    tz_name = CONFIG.get("display_timezone", "America/New_York") if 'CONFIG' in globals() else "America/New_York"
    if ZoneInfo is None:
        return timezone.utc  # Fallback if zoneinfo not available
    try:
        return ZoneInfo(tz_name)
    except (KeyError, ValueError):
        # KeyError: timezone not in database, ValueError: invalid tz string
        return ZoneInfo("America/New_York")  # Default if invalid

def local_now():
    """Get current time in the configured display timezone for user-facing reports."""
    return datetime.now(get_display_tz())

# === SECURITY: IP ADDRESS VALIDATION ===
def validate_miner_ip(ip_str):
    """
    Validate that an IP address is safe to connect to (SSRF prevention).
    Only allows private network IPs that would be expected for local miners.
    Returns True if IP is valid and safe, False otherwise.
    """
    if not ip_str or not isinstance(ip_str, str):
        return False

    # Strip whitespace and validate format
    ip_str = ip_str.strip()

    # Basic format check
    if not re.match(r'^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$', ip_str):
        return False

    try:
        ip = ipaddress.ip_address(ip_str)

        # SECURITY: Only allow private network IPs (RFC 1918)
        # This prevents SSRF attacks to external services
        if not ip.is_private:
            logger.warning(f"SECURITY: Rejecting non-private IP: {ip_str}")
            return False

        # SECURITY: Block localhost to prevent self-attacks
        if ip.is_loopback:
            logger.warning(f"SECURITY: Rejecting loopback IP: {ip_str}")
            return False

        # SECURITY: Block link-local addresses (169.254.x.x)
        if ip.is_link_local:
            logger.warning(f"SECURITY: Rejecting link-local IP: {ip_str}")
            return False

        # SECURITY: Block multicast and reserved ranges
        if ip.is_multicast or ip.is_reserved:
            logger.warning(f"SECURITY: Rejecting multicast/reserved IP: {ip_str}")
            return False

        return True
    except ValueError:
        return False

def _http(url, timeout=10, headers=None, retries=2):
    """HTTP GET with automatic retry on network failures for self-healing."""
    h = {"User-Agent": f"SpiralSentinel/{__version__}"}
    if headers: h.update(headers)

    for attempt in range(retries + 1):
        try:
            req = urllib.request.Request(url, headers=h)
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return json.loads(resp.read().decode())
        except (urllib.error.HTTPError, json.JSONDecodeError):
            # HTTP 4xx/5xx or parsing error - don't retry
            return None
        except (urllib.error.URLError, socket.timeout, OSError) as e:
            # Network error - retry with brief delay
            if attempt < retries:
                time.sleep(1 * (attempt + 1))
                continue
            return None
    return None


_RPC_ALLOWED_METHODS = frozenset({
    "getmininginfo", "getblockchaininfo", "getnetworkinfo", "getpeerinfo",
    "getblockcount", "getdifficulty", "getconnectioncount", "getmempoolinfo",
    "getnettotals", "uptime", "getbestblockhash",
})

def _rpc_call(host, port, method, params=None, timeout=10):
    """Make an RPC call to a Bitcoin-like daemon.

    Note: This assumes no authentication is required (cookie auth or no auth).
    For authenticated RPC, the pool handles this - we use pool API as primary.
    """
    # SECURITY: Only allow RPC to localhost
    if host not in ("127.0.0.1", "localhost"):
        logger.warning(f"SECURITY: Rejecting RPC call to non-localhost host: {host}")
        return None
    # SECURITY: Validate method against whitelist
    if method not in _RPC_ALLOWED_METHODS:
        logger.warning(f"SECURITY: Rejecting disallowed RPC method: {method}")
        return None
    try:
        url = f"http://{host}:{port}"
        payload = {
            "jsonrpc": "1.0",
            "id": "sentinel",
            "method": method,
            "params": params or []
        }
        req = urllib.request.Request(
            url,
            data=json.dumps(payload).encode(),
            headers={"Content-Type": "application/json"}
        )
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            result = json.loads(resp.read().decode())
            if "result" in result:
                return result["result"]
            return None
    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError, json.JSONDecodeError):
        return None

def utc_ts(): return datetime.now(timezone.utc).isoformat()

_api_cache = {}
def _cached_fetch(key, fetch_func, ttl_seconds=300):
    now = time.time()
    if key in _api_cache:
        cached_time, cached_data = _api_cache[key]
        if now - cached_time < ttl_seconds: return cached_data
    result = fetch_func()
    _api_cache[key] = (now, result)
    return result

# === BLOCKCHAIN SYNC CHECK ===
def check_blockchain_synced():
    """Check if blockchain node is fully synced before sending notifications.

    Uses multiple methods to verify sync status:
    1. Pool API blockHeight (quick check)
    2. Pool API connectedPeers (verify node is connected)
    3. Pool API networkDifficulty (verify node is receiving data)

    Returns True only when we have high confidence the node is synced.
    """
    try:
        pool_stats = fetch_pool_stats()
        if not pool_stats:
            logger.debug("Blockchain sync check: fetch_pool_stats returned None")
            return False

        pool_data = pool_stats.get("poolStats", {})
        block_height = pool_data.get("blockHeight", 0)
        connected_peers = pool_data.get("connectedPeers", 0)
        network_diff = pool_data.get("networkDifficulty", 0)

        # Check 1: Must have a valid block height
        if block_height <= 0:
            logger.debug(f"Blockchain sync check: blockHeight={block_height} (waiting for sync)")
            return False

        # Check 2: Should have at least 1 peer (or we're not on the network)
        # Note: Some setups may not expose peers, so we don't fail hard here
        if connected_peers == 0:
            logger.debug(f"Blockchain sync check: connectedPeers=0 (may still be synced)")

        # Check 3: Network difficulty should be set (indicates we're receiving blocks)
        if network_diff <= 0:
            logger.debug(f"Blockchain sync check: networkDifficulty={network_diff} (waiting for data)")
            return False

        logger.info(f"Blockchain synced (height: {block_height:,}, difficulty: {format_difficulty(network_diff)}, peers: {connected_peers})")
        return True

    except Exception as e:
        logger.warning(f"Blockchain sync check error: {e}")
        return False


def check_pool_health():
    """Verify pool is healthy and functioning, not just responding.

    Returns dict with health status:
    - healthy: True if pool is fully operational
    - block_height: Current block height (0 if unknown)
    - network_diff: Network difficulty (0 if unknown)
    - hashrate: Pool hashrate (0 if unknown)
    - miners: Number of connected miners (0 if unknown)
    - reason: Human-readable status/error message
    """
    try:
        pool_stats = fetch_pool_stats()
        if not pool_stats:
            return {"healthy": False, "reason": "Pool API not responding", "block_height": 0, "network_diff": 0, "hashrate": 0, "miners": 0}

        pool_data = pool_stats.get("poolStats", {})
        block_height = pool_data.get("blockHeight", 0)
        network_diff = pool_data.get("networkDifficulty", 0)
        pool_hashrate = pool_data.get("poolHashrate", 0)
        connected_miners = pool_data.get("connectedMiners", 0)

        # Determine health status
        issues = []
        if block_height <= 0:
            issues.append("no block height")
        if network_diff <= 0:
            issues.append("no network difficulty")

        if issues:
            return {
                "healthy": False,
                "reason": f"Pool unhealthy: {', '.join(issues)}",
                "block_height": block_height,
                "network_diff": network_diff,
                "hashrate": pool_hashrate,
                "miners": connected_miners
            }

        return {
            "healthy": True,
            "reason": "Pool fully operational",
            "block_height": block_height,
            "network_diff": network_diff,
            "hashrate": pool_hashrate,
            "miners": connected_miners
        }

    except Exception as e:
        return {"healthy": False, "reason": f"Health check error: {e}", "block_height": 0, "network_diff": 0, "hashrate": 0, "miners": 0}


# Global sync state - prevents notification spam during initial sync
_blockchain_synced = False
_last_sync_check = 0
_SYNC_CHECK_INTERVAL = 60  # Check sync status every 60 seconds

def is_blockchain_ready():
    """Check if blockchain is synced (with caching to reduce RPC calls)"""
    global _blockchain_synced, _last_sync_check
    now = time.time()
    if now - _last_sync_check > _SYNC_CHECK_INTERVAL:
        _blockchain_synced = check_blockchain_synced()
        _last_sync_check = now
    return _blockchain_synced

# === CONFIGURATION ===
# Primary location: ~/.spiralsentinel/ (user home directory)
# Fallback location: $INSTALL_DIR/config/sentinel/ (when ProtectHome=yes blocks access)
# systemd's ProtectHome=yes replaces /home with empty tmpfs; ReadWritePaths can override
# this but ONLY if the directory exists on disk before service start. If it doesn't,
# the bind mount fails silently and we get PermissionError. Fall back gracefully.
_home_sentinel_dir = Path.home() / ".spiralsentinel"
try:
    _home_sentinel_dir.mkdir(parents=True, exist_ok=True)
    CONFIG_FILE = _home_sentinel_dir / "config.json"
    DATA_DIR = _home_sentinel_dir
except (PermissionError, OSError):
    # ProtectHome=yes blocking access — fall back to install directory
    _fallback_dir = INSTALL_DIR / "config" / "sentinel"
    try:
        _fallback_dir.mkdir(parents=True, exist_ok=True)
    except (PermissionError, OSError) as _e:
        # Log at import time — logger may not be configured yet, use print
        print(f"[SENTINEL] WARNING: Could not create fallback config dir {_fallback_dir}: {_e}")
    CONFIG_FILE = _fallback_dir / "config.json"
    DATA_DIR = _fallback_dir
PAUSE_FILE = DATA_DIR / "maintenance_pause"

# Shared data directory - accessible by both admin user and dashboard service
SHARED_DATA_DIR = INSTALL_DIR / "data"

# === MAINTENANCE MODE ===
def is_maintenance_mode():
    """Check if maintenance mode is active (alerts paused).

    Returns tuple: (is_paused, minutes_remaining, reason)
    """
    if not PAUSE_FILE.exists():
        return False, 0, None

    try:
        with open(PAUSE_FILE) as f:
            data = json.load(f)

        pause_until = data.get("pause_until", 0)
        reason = data.get("reason", "Scheduled maintenance")

        now = time.time()
        if now >= pause_until:
            # Pause expired, remove file
            PAUSE_FILE.unlink(missing_ok=True)
            return False, 0, None

        minutes_remaining = int((pause_until - now) / 60)
        return True, minutes_remaining, reason
    except (json.JSONDecodeError, IOError, OSError, KeyError):
        # Invalid pause file, remove it
        try:
            PAUSE_FILE.unlink(missing_ok=True)
        except (OSError, PermissionError):
            pass
        return False, 0, None

def set_maintenance_mode(minutes, reason="Scheduled maintenance"):
    """Enable maintenance mode for specified minutes."""
    try:
        DATA_DIR.mkdir(parents=True, exist_ok=True)
    except (PermissionError, OSError):
        pass
    pause_until = time.time() + (minutes * 60)
    try:
        _atomic_json_save(PAUSE_FILE, {
            "pause_until": pause_until,
            "pause_start": time.time(),
            "duration_minutes": minutes,
            "reason": reason
        }, indent=2)
    except (PermissionError, OSError) as e:
        logger.warning(f"Could not write maintenance pause file: {e}")
        return False
    return True

def clear_maintenance_mode():
    """Clear maintenance mode (resume alerts)."""
    try:
        PAUSE_FILE.unlink(missing_ok=True)
        return True
    except (OSError, PermissionError):
        return False

# === HA COORDINATION FOR SENTINEL ===
# In HA mode with multiple pool nodes, each node runs its own Sentinel.
# To prevent triple-alerting (3 nodes = 3x alerts), we coordinate:
# - Only the MASTER Sentinel sends Discord/Telegram alerts
# - BACKUP Sentinels still monitor but suppress alerts
# - If master fails, the new master's Sentinel takes over alerting
# - Maintenance mode is checked from the unified maintenance file

# Import HA manager if available
try:
    from ha_manager import HAManager
    _HA_AVAILABLE = True
except ImportError:
    _HA_AVAILABLE = False

# HA state for alert coordination
_ha_manager = None
_ha_check_interval = 30  # Check HA status every 30 seconds
_last_ha_check = 0
_cached_ha_role = "STANDALONE"  # Safe default: assume standalone until HA confirmed
_node_uuid = None

def get_node_uuid() -> str:
    """Get this node's unique identifier.

    Returns:
        str: 16-character hexadecimal UUID for this node
    """
    global _node_uuid
    if _node_uuid:
        return _node_uuid

    uuid_file = INSTALL_DIR / "config" / "node-uuid"
    if uuid_file.exists():
        try:
            raw_uuid = uuid_file.read_text().strip()
            # SECURITY: Validate proper UUID format (standard UUID v4 format)
            # Format: 8-4-4-4-12 hex characters (e.g., 550e8400-e29b-41d4-a716-446655440000)
            # Also accept 16-32 char hex strings for backward compatibility with generated IDs
            import re
            uuid_pattern = r'^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$'
            hex_pattern = r'^[a-f0-9]{16,32}$'
            if re.match(uuid_pattern, raw_uuid, re.IGNORECASE) or re.match(hex_pattern, raw_uuid, re.IGNORECASE):
                _node_uuid = raw_uuid
                return _node_uuid
            else:
                logger.warning("Invalid UUID format in node-uuid file, regenerating")
        except (IOError, OSError):
            pass

    # Generate from hostname + machine-id
    import hashlib
    hostname = socket.gethostname()
    machine_id = ""
    try:
        machine_id = Path("/etc/machine-id").read_text().strip()
    except (IOError, OSError):
        machine_id = str(time.time())

    _node_uuid = hashlib.sha256(f"{hostname}-{machine_id}".encode()).hexdigest()[:16]
    return _node_uuid

def init_ha_manager() -> object:
    """Initialize HA manager for alert coordination.

    Returns:
        HAManager: The initialized HA manager, or None if not available
    """
    global _ha_manager

    if not _HA_AVAILABLE:
        return None

    try:
        _ha_manager = HAManager()
        if _ha_manager.is_ha_enabled():
            logger.info("HA mode detected - coordinating alerts with cluster")
            logger.info(f"   Node UUID: {get_node_uuid()}")
        return _ha_manager
    except Exception as e:
        logger.warning(f"HA manager init failed: {e}")
        return None

def get_ha_role():
    """Get this node's current HA role (MASTER, BACKUP, or OBSERVER).

    Returns:
        str: Current role, or "STANDALONE" if HA not enabled
    """
    global _ha_manager, _last_ha_check, _cached_ha_role

    if not _HA_AVAILABLE or not _ha_manager:
        return "STANDALONE"

    now = time.time()
    if now - _last_ha_check < _ha_check_interval:
        return _cached_ha_role

    _last_ha_check = now

    try:
        status = _ha_manager.get_status()
        if status and status.enabled:
            _cached_ha_role = status.local_role
            return _cached_ha_role
        elif status and not status.enabled:
            # HA explicitly disabled via API response — safe to mark STANDALONE
            _cached_ha_role = "STANDALONE"
            return "STANDALONE"
        else:
            # API unavailable (returned None) — keep last known role to prevent
            # dual-master alerts. If both nodes lose API and assume STANDALONE,
            # both would think they're master and send duplicate alerts.
            return _cached_ha_role or "STANDALONE"
    except Exception:
        return _cached_ha_role or "STANDALONE"

def is_master_sentinel():
    """Check if this Sentinel instance should send alerts.

    In HA mode, only the MASTER node's Sentinel sends alerts.
    In standalone mode, always returns True.

    Returns:
        bool: True if this Sentinel should send alerts
    """
    role = get_ha_role()

    # Standalone mode - always send alerts
    if role == "STANDALONE":
        return True

    # HA mode - only master sends alerts
    return role == "MASTER"

def check_ha_maintenance_propagation() -> tuple:
    """Check if any node in the HA cluster has maintenance mode active.

    This allows maintenance mode to propagate across all nodes.
    When one node enters maintenance, all Sentinels suppress alerts.

    Returns:
        tuple: (is_maintenance: bool, minutes_remaining: int, reason: str, source_node: str)
    """
    # First check local maintenance file
    local_maintenance, local_mins, local_reason = is_maintenance_mode()
    if local_maintenance:
        return True, local_mins, local_reason, "local"

    # Check unified maintenance file (used by maintenance-mode.sh)
    unified_file = INSTALL_DIR / "config" / ".maintenance-mode"
    if unified_file.exists():
        try:
            with open(unified_file) as f:
                data = json.load(f)

            # SECURITY: Validate JSON structure before using
            required_fields = ["end_time"]
            if not all(field in data for field in required_fields):
                logger.warning("Invalid maintenance file structure, ignoring")
            else:
                end_time = data.get("end_time", 0)
                # SECURITY: Validate end_time is a number
                if not isinstance(end_time, (int, float)):
                    logger.warning("Invalid end_time type in maintenance file")
                else:
                    now = time.time()

                    if now < end_time:
                        mins_remaining = int((end_time - now) / 60)
                        reason = str(data.get("reason", "Scheduled maintenance"))[:200]  # Limit length
                        source_node = str(data.get("node_uuid", "unknown"))[:16]  # Limit length
                        return True, mins_remaining, reason, f"node-{source_node[:8]}"
        except (json.JSONDecodeError, IOError, OSError, TypeError) as e:
            # Log but don't fail - maintenance check should be resilient
            logger.debug("Error reading maintenance file: %s", e)

    # If HA mode, check if cluster has maintenance mode
    if _ha_manager and _HA_AVAILABLE:
        try:
            status = _ha_manager.get_status()
            if status and status.enabled:
                # Could query other nodes via HA status API
                # For now, rely on shared filesystem or unified maintenance file
                pass
        except Exception as e:
            logger.debug(f"Failed to check HA maintenance status: {e}")

    return False, 0, None, None

def create_ha_status_embed():
    """Create Discord embed showing HA cluster status."""
    if not _ha_manager or not _HA_AVAILABLE:
        return None

    try:
        status = _ha_manager.get_status()
        if not status or not status.enabled:
            return None

        role_emoji = {"MASTER": "👑", "BACKUP": "🔄", "OBSERVER": "👁️"}.get(status.local_role, "❓")
        state_color = {"running": 0x00FF00, "election": 0xFFFF00, "failover": 0xFFA500, "degraded": 0xFF0000}.get(status.state, 0x808080)

        fields = [
            {"name": "Role", "value": f"{role_emoji} {status.local_role}", "inline": True},
            {"name": "State", "value": status.state.upper(), "inline": True},
            {"name": "VIP", "value": status.vip or "N/A", "inline": True},
            {"name": "Cluster", "value": f"{len(status.nodes)} nodes", "inline": True},
            {"name": "Failovers", "value": str(status.failover_count), "inline": True},
        ]

        return _embed(
            theme("ha.status.title"),
            theme("ha.status.body", node=_SENTINEL_HOSTNAME or get_node_uuid()[:8], role=status.local_role),
            state_color,
            fields,
            footer=theme("ha.status.footer", vip=status.vip or "N/A")
        )
    except Exception:
        return None

DEFAULT_CONFIG = {
    # Discord notifications (optional)
    "discord_webhook_url": "",
    # Telegram notifications (optional)
    "telegram_bot_token": "",          # Get from @BotFather on Telegram
    "telegram_chat_id": "",            # Your chat/group/channel ID
    "telegram_enabled": False,         # Enable Telegram notifications
    "telegram_commands_enabled": True, # Enable /status /miners /hashrate /blocks bot commands (requires telegram_enabled)
    # XMPP notifications (optional) — requires slixmpp (pip install slixmpp)
    "xmpp_jid": "",                    # Bot's JID (e.g. sentinel@yourserver.com)
    "xmpp_password": "",               # Bot's password
    "xmpp_recipient": "",              # Recipient JID (user@server.com or room@conference.server.com)
    "xmpp_use_tls": True,             # Use TLS encryption
    "xmpp_muc": False,                # True if recipient is a MUC (group chat) room
    "xmpp_enabled": False,            # Enable XMPP notifications
    # ntfy notifications (optional) — free push notifications, no account required
    "ntfy_url": "",                    # ntfy topic URL (e.g. https://ntfy.sh/your_topic or https://your-ntfy-server.com/your_topic)
    "ntfy_token": "",                  # Optional auth token (required for private topics or self-hosted servers)
    # Email / SMTP notifications (optional) — stored in config.json (chmod 600, spiraluser only)
    "smtp_host": "",                   # SMTP server hostname (e.g. smtp.gmail.com)
    "smtp_port": 587,                  # 587 = STARTTLS (recommended), 465 = SSL/TLS, 25 = plain
    "smtp_username": "",               # SMTP login username / email address
    "smtp_password": "",               # SMTP login password or app-specific password
    "smtp_from": "",                   # From address (defaults to smtp_username if blank)
    "smtp_to": "",                     # Recipient address(es) — comma-separated for multiple
    "smtp_use_tls": True,              # Use STARTTLS (port 587). Set False for SSL (port 465)
    "smtp_enabled": False,             # Enable email notifications
    # Sentinel health endpoint — lightweight HTTP status on loopback
    "sentinel_health_enabled": True,   # Expose GET /health and GET /cooldowns on localhost
    "sentinel_health_port": 9191,      # Port for health endpoint (loopback only, not exposed externally)
    # General settings
    "wallet_address": "YOUR_DGB_ADDRESS",  # Legacy: single-coin DGB address
    "check_interval": 120,
    "report_hours": [6, 12, 18],
    "final_report_time": "21:55",  # Last report before quiet hours (HH:MM format, or null to disable)
    "major_report_hour": 6,
    "weekly_report_day": 0,
    "monthly_report_day": 1,
    "quiet_hours_start": 22,
    "quiet_hours_end": 6,
    "miner_offline_threshold_min": 10,
    "temp_warning": 75,
    "temp_critical": 85,
    "health_warn_threshold": 70,
    "net_drop_threshold_phs": 48,
    "net_reset_threshold_phs": 52,
    "auto_restart_enabled": True,
    "auto_restart_min_offline": 20,
    "auto_restart_cooldown": 1800,
    "blip_detection_enabled": True,
    "expected_fleet_ths": 22.0,
    "sats_change_alert_pct": 15,
    # Sats surge tracking - alerts when a coin's sat value increases 25%+ over 1 week
    "sats_surge_enabled": True,            # Enable sats surge alerts
    "sats_surge_threshold_pct": 25,        # Alert when sat value increases by this % over baseline
    "sats_surge_lookback_days": 7,         # Compare against sat value from N days ago
    "sats_surge_sample_interval": 3600,    # Record sat values every N seconds (1 hour)
    "sats_surge_cooldown_hours": 24,       # Don't re-alert for same coin within N hours
    "odds_alert_threshold": 40,
    # Historical data settings
    "history_sample_interval": 900,  # 15 minutes between samples
    "history_max_age_days": 730,     # 2 years of data retention
    "history_disk_budget_mb": 50,    # Recommended disk space for history (~15MB actual)
    # Startup alert suppression - suppresses alerts during initial startup
    # SECURITY: Per-alert-type configuration prevents blind spots during restarts
    "startup_alert_suppression_min": 30,  # Default suppression for non-critical alerts (aligned with coordinator grace period)
    "startup_suppression_bypass": [       # Alert types that ALWAYS bypass startup suppression
        "block_found",                    # Always celebrate blocks!
        "startup_summary",                # Expected at startup
        "temp_critical",                  # Critical temps should never be suppressed
        "6h_report",                      # Scheduled reports should not be missed due to startup
        "weekly_report",                  # Scheduled reports should not be missed due to startup
        "monthly_earnings",               # Scheduled reports should not be missed due to startup
        "quarterly_report",               # Scheduled reports should not be missed due to startup
    ],
    # ═══════════════════════════════════════════════════════════════════════════════
    # ALERT THROTTLING - Prevents spammy repeated alerts
    # ═══════════════════════════════════════════════════════════════════════════════
    # Set to 0 to disable throttling for a specific alert type
    # Values are in seconds (e.g., 3600 = 1 hour)
    "alert_cooldowns": {
        "hashrate_crash": 21600,     # 6 hours - network crash alerts
        "miner_offline": 0,          # No cooldown - always alert immediately
        "miner_online": 0,           # No cooldown - always alert immediately
        "miner_reboot": 600,         # 10 min - reboot detection
        "temp_warning": 3600,        # 1 hour - temperature warnings
        "temp_critical": 0,          # No cooldown - always alert critical temps
        "zombie_miner": 3600,        # 1 hour - zombie detection
        "degradation": 3600,         # 1 hour - hashrate degradation
        "power_event": 600,          # 10 min - fleet power events
        "pool_hashrate_drop": 1800,  # 30 min - pool hashrate drop
        "block_found": 0,            # No cooldown - always celebrate blocks!
        "sats_surge": 0,             # No cooldown - surge check has internal cooldown per coin
        "wallet_drop": 3600,         # 1 hour - prevent repeated alerts on fluctuating balance
        "dry_streak": 21600,         # 6 hours - extended dry streak without blocks
        "difficulty_change": 3600,   # 1 hour - network difficulty change alerts
        "disk_warning": 3600,        # 1 hour - disk space warning
        "disk_critical": 300,        # 5 min - disk space critical
        "mempool_congestion": 3600,  # 1 hour - BTC mempool congestion
        "backup_stale": 86400,       # 24 hours - stale backup alert (re-alert daily)
    },
    # Wallet balance drop alert - alerts when solo mining wallet loses funds unexpectedly
    "wallet_drop_alert_enabled": True,
    # Report frequency settings
    "report_frequency": "6h",        # "6h" (4x daily), "daily" (1x daily), or "off" (disabled)
    # ═══════════════════════════════════════════════════════════════════════════════
    # ALERT BATCHING - Combines multiple alerts into digest notifications
    # ═══════════════════════════════════════════════════════════════════════════════
    # When enabled, miner alerts (offline, temp, reboot) are collected within a time
    # window and sent as a single "digest" alert. This dramatically reduces notification
    # spam when multiple issues occur simultaneously (e.g., power outage affects all miners).
    #
    # Alert types that are ALWAYS sent immediately (never batched):
    #   - block_found (you always want to know immediately!)
    #   - startup_summary, 6h_report, weekly_report, monthly_earnings
    #   - hashrate_crash, high_odds (network-wide events)
    #
    # Alert types that ARE batched when enabled:
    #   - miner_offline, miner_online, miner_reboot
    #   - temp_warning, temp_critical
    #   - degradation, zombie_miner, excessive_restarts
    "alert_batching_enabled": True,  # Enable alert batching/aggregation
    "alert_batch_window_seconds": 300,  # 5 minute window to collect related alerts
    # Individual report toggles (all enabled by default)
    "enable_6h_reports": True,       # 6-hour or daily intel reports
    "enable_weekly_reports": True,   # Weekly summary reports
    "enable_monthly_reports": True,  # Monthly earnings reports
    "enable_quarterly_reports": True, # Quarterly reports
    # Pool API settings for share validation
    "pool_api_url": "http://localhost:4000",  # Spiral Stratum API
    "pool_admin_api_key": "",                  # Admin API key for device hints (from pool config)
    "dashboard_api_key": "",                   # Dashboard API key for authenticated dashboard calls
    "pool_id": "dgb_sha256_1",                 # Legacy: single-coin pool ID (uses _1 suffix)
    "pool_share_validation": True,             # Enable pool-side share verification
    "pool_no_shares_threshold_min": 30,        # Alert if no pool shares for this long
    "push_device_hints": True,                 # Push device info to pool for difficulty hints
    # ═══════════════════════════════════════════════════════════════════════════════
    # PROMETHEUS METRICS & INFRASTRUCTURE MONITORING
    # ═══════════════════════════════════════════════════════════════════════════════
    # Fetches metrics from the Spiral Stratum Go backend for infrastructure health
    "metrics_enabled": True,                   # Enable Prometheus metrics fetching
    "metrics_url": "http://localhost:9100/metrics",  # Prometheus metrics endpoint
    "metrics_token": "",                       # Bearer token for metrics auth (from SPIRAL_METRICS_TOKEN)
    "metrics_fetch_interval": 60,              # Fetch metrics every N seconds
    # Infrastructure health thresholds
    "infra_circuit_breaker_alert": True,       # Alert on circuit breaker state changes
    "infra_backpressure_alert": True,          # Alert on high backpressure (level >= 2)
    "infra_zmq_health_alert": True,            # Alert on ZMQ degradation (health > 2)
    "infra_wal_errors_alert": True,            # Alert on WAL write/commit errors
    "infra_share_loss_alert": True,            # Alert on share batch drops
    # Currency display settings (defaults match installer)
    "report_currency": "CAD",                  # Any supported currency code
    "power_currency": "CAD",                   # Currency for power costs
    "power_rate_kwh": 0.12,                    # Electricity rate per kWh
    # Timezone for reports and alerts (internal timestamps always use UTC)
    "display_timezone": "America/New_York",    # IANA timezone for user-facing times
    # Alert theme: "cyberpunk" (skulls, choom, edgy) or "professional" (clean, enterprise)
    "alert_theme": "cyberpunk",
    # ═══════════════════════════════════════════════════════════════════════════════
    # SCHEDULED MAINTENANCE WINDOWS — Suppress alerts during planned downtime
    # ═══════════════════════════════════════════════════════════════════════════════
    # Each window: {"start": "HH:MM", "end": "HH:MM", "days": [0-6], "reason": "..."}
    # days: 0=Monday, 6=Sunday. Omit "days" key to apply every day.
    # Example — weekly backup window every Sunday 02:00–04:00:
    #   {"start": "02:00", "end": "04:00", "days": [6], "reason": "Weekly backup"}
    "scheduled_maintenance_windows": [],
    # ═══════════════════════════════════════════════════════════════════════════════
    # DRY STREAK ALERTING — Alert when no block found for an extended period
    # ═══════════════════════════════════════════════════════════════════════════════
    "dry_streak_enabled": True,            # Alert when no block found for too long
    "dry_streak_multiplier": 3,            # Alert after N × expected interval without a block
    # ═══════════════════════════════════════════════════════════════════════════════
    # NETWORK DIFFICULTY CHANGE ALERTS — Alert on significant difficulty swings
    # ═══════════════════════════════════════════════════════════════════════════════
    "difficulty_alert_enabled": True,      # Alert on large difficulty changes
    "difficulty_alert_threshold_pct": 25,  # Alert when difficulty drifts this % from the baseline at last alert (not tick-to-tick)
    # ═══════════════════════════════════════════════════════════════════════════════
    # DISK SPACE MONITORING — Alert when disk usage exceeds thresholds
    # ═══════════════════════════════════════════════════════════════════════════════
    "disk_monitor_enabled": True,          # Enable disk space monitoring
    "disk_warn_pct": 85,                   # Warning threshold (%)
    "disk_critical_pct": 95,              # Critical threshold (%)
    "disk_monitor_paths": ["/", "/spiralpool", "/var"],  # Mount points to monitor
    # ═══════════════════════════════════════════════════════════════════════════════
    # MEMPOOL CONGESTION ALERT (BTC) — Alert when Bitcoin mempool is congested
    # ═══════════════════════════════════════════════════════════════════════════════
    "mempool_alert_enabled": True,         # Enable BTC mempool congestion alerting
    "mempool_alert_threshold": 50000,      # Alert when mempool exceeds N transactions
    # ═══════════════════════════════════════════════════════════════════════════════
    # BACKUP STALENESS ALERT — Alert when no recent backup exists
    # ═══════════════════════════════════════════════════════════════════════════════
    "backup_stale_enabled": True,          # Alert if newest backup is older than threshold
    "backup_stale_days": 2,                # Days before backup is considered stale
    # ═══════════════════════════════════════════════════════════════════════════════
    # HIGH AVAILABILITY (HA) TUNING
    # ═══════════════════════════════════════════════════════════════════════════════
    "ha_role_change_confirm_secs": 90,     # Seconds a role change must hold before alerting.
                                           # Suppresses brief keepalived VRRP election blips
                                           # (MASTER→BACKUP→MASTER within N seconds).
                                           # Real failovers hold indefinitely; blips self-resolve.
    # ═══════════════════════════════════════════════════════════════════════════════
    # MULTI-COIN SUPPORT (V2) + AUTO-DETECTION
    # ═══════════════════════════════════════════════════════════════════════════════
    # AUTO-DETECTION: When running alongside Spiral Pool, Sentinel will automatically
    # detect which coin(s) are configured from the pool API. No manual config needed!
    #
    # For multi-coin mode, set "multi_coin_enabled" to True and configure coins below.
    # Each coin has its own pool_id, wallet, and stratum ports.
    "multi_coin_enabled": False,               # Set True for explicit multi-coin config
    "coins": [
        # DigiByte (disabled by default - user enables during install)
        {
            "symbol": "DGB",
            "name": "DigiByte",
            "enabled": False,
            "pool_id": "dgb_sha256_1",
            "wallet_address": "YOUR_DGB_ADDRESS",
            "stratum_port": 3333,
            "stratum_v2_port": 3334,
            "rpc_port": 14022,
            "zmq_port": 28532,
        },
        # Bitcoin (disabled by default - user enables during install)
        {
            "symbol": "BTC",
            "name": "Bitcoin",
            "enabled": False,
            "pool_id": "btc_sha256_1",
            "wallet_address": "YOUR_BTC_ADDRESS",
            "stratum_port": 4333,
            "stratum_v2_port": 4334,
            "rpc_port": 8332,
            "zmq_port": 28332,
        },
        # Bitcoin Cash (disabled by default - user enables during install)
        {
            "symbol": "BCH",
            "name": "Bitcoin Cash",
            "enabled": False,
            "pool_id": "bch_sha256_1",
            "wallet_address": "YOUR_BCH_ADDRESS",
            "stratum_port": 5333,
            "stratum_v2_port": 5334,
            "rpc_port": 8432,
            "zmq_port": 28432,
        },
        # Bitcoin II (disabled by default - user enables during install)
        # WARNING: BC2 uses identical address formats to Bitcoin (bc1q, 1, 3)
        # Ensure your wallet address is from Bitcoin II Core, NOT Bitcoin Core!
        {
            "symbol": "BC2",
            "name": "Bitcoin II",
            "enabled": False,
            "pool_id": "bc2_sha256_1",
            "wallet_address": "YOUR_BC2_ADDRESS",
            "stratum_port": 6333,
            "stratum_v2_port": 6334,
            "rpc_port": 8339,
            "zmq_port": 28338,
        },
        # Namecoin (disabled by default - merge-mineable with Bitcoin)
        # First coin to implement AuxPoW (merged mining) - mine NMC while mining BTC
        {
            "symbol": "NMC",
            "name": "Namecoin",
            "enabled": False,
            "pool_id": "nmc_sha256_1",
            "wallet_address": "YOUR_NMC_ADDRESS",
            "stratum_port": 14335,
            "stratum_v2_port": 14336,
            "rpc_port": 8336,
            "zmq_port": 28336,
        },
        # Syscoin (disabled by default - merge-mineable with Bitcoin)
        # UTXO platform with Z-DAG for fast confirmations
        {
            "symbol": "SYS",
            "name": "Syscoin",
            "enabled": False,
            "pool_id": "sys_sha256_1",
            "wallet_address": "YOUR_SYS_ADDRESS",
            "stratum_port": 15335,
            "stratum_v2_port": 15336,
            "rpc_port": 8370,
            "zmq_port": 28370,
        },
        # Myriad (disabled by default - merge-mineable with Bitcoin via SHA256d algo)
        # Multi-algorithm coin: SHA256d, Scrypt, Myr-Groestl, Skein, Yescrypt
        {
            "symbol": "XMY",
            "name": "Myriad",
            "enabled": False,
            "pool_id": "xmy_sha256_1",
            "wallet_address": "YOUR_XMY_ADDRESS",
            "stratum_port": 17335,
            "stratum_v2_port": 17336,
            "rpc_port": 10889,
            "zmq_port": 28889,
        },
        # Fractal Bitcoin (disabled by default - merge-mineable with Bitcoin)
        {
            "symbol": "FBTC",
            "name": "Fractal Bitcoin",
            "enabled": False,
            "pool_id": "fbtc_sha256_1",
            "wallet_address": "YOUR_FBTC_ADDRESS",
            "stratum_port": 18335,
            "stratum_v2_port": 18336,
            "rpc_port": 8340,
            "zmq_port": 28340,
        },
        # Litecoin (disabled by default - user enables during install)
        {
            "symbol": "LTC",
            "name": "Litecoin",
            "enabled": False,
            "pool_id": "ltc_scrypt_1",
            "wallet_address": "YOUR_LTC_ADDRESS",
            "stratum_port": 7333,
            "stratum_v2_port": 7334,
            "rpc_port": 9332,
            "zmq_port": 28933,
        },
        # Dogecoin (disabled by default - user enables during install)
        {
            "symbol": "DOGE",
            "name": "Dogecoin",
            "enabled": False,
            "pool_id": "doge_scrypt_1",
            "wallet_address": "YOUR_DOGE_ADDRESS",
            "stratum_port": 8335,
            "stratum_v2_port": 8337,
            "rpc_port": 22555,
            "zmq_port": 28555,
        },
        # DigiByte Scrypt (disabled by default - uses same node as DGB SHA-256d)
        {
            "symbol": "DGB-SCRYPT",
            "name": "DigiByte (Scrypt)",
            "enabled": False,
            "pool_id": "dgb_scrypt_1",
            "wallet_address": "YOUR_DGB_ADDRESS",
            "stratum_port": 3336,
            "stratum_v2_port": 3337,
            "rpc_port": 14022,
            "zmq_port": 28532,
        },
        # PepeCoin (disabled by default - user enables during install)
        {
            "symbol": "PEP",
            "name": "PepeCoin",
            "enabled": False,
            "pool_id": "pep_scrypt_1",
            "wallet_address": "YOUR_PEP_ADDRESS",
            "stratum_port": 10335,
            "stratum_v2_port": 10336,
            "rpc_port": 33873,
            "zmq_port": 28873,
        },
        # Catcoin (disabled by default - user enables during install)
        {
            "symbol": "CAT",
            "name": "Catcoin",
            "enabled": False,
            "pool_id": "cat_scrypt_1",
            "wallet_address": "YOUR_CAT_ADDRESS",
            "stratum_port": 12335,
            "stratum_v2_port": 12336,
            "rpc_port": 9932,
            "zmq_port": 28932,
        },
    ],
}

def validate_config(config):
    """
    Validate configuration values for consistency and safety.
    Returns list of warnings/errors found.
    """
    issues = []

    # Temperature thresholds: warning must be less than critical
    temp_warn = config.get("temp_warning", 75)
    temp_crit = config.get("temp_critical", 85)
    if temp_warn >= temp_crit:
        issues.append(f"⚠️ Config: temp_warning ({temp_warn}) should be less than temp_critical ({temp_crit})")
        # Auto-fix: swap them
        config["temp_warning"] = min(temp_warn, temp_crit) - 5
        config["temp_critical"] = max(temp_warn, temp_crit)

    # Cooldowns must be non-negative
    cooldowns = config.get("alert_cooldowns", {})
    for key, val in cooldowns.items():
        if not isinstance(val, (int, float)) or val < 0:
            issues.append(f"⚠️ Config: alert_cooldowns.{key} must be >= 0, got {val}")
            cooldowns[key] = 0

    # Check interval must be positive
    interval = config.get("check_interval", 120)
    if not isinstance(interval, (int, float)) or interval < 10:
        issues.append(f"⚠️ Config: check_interval must be >= 10 seconds, got {interval}")
        config["check_interval"] = max(10, interval) if isinstance(interval, (int, float)) else 120

    # Quiet hours must be valid 0-23
    qh_start = config.get("quiet_hours_start", 22)
    qh_end = config.get("quiet_hours_end", 6)
    if not (0 <= qh_start <= 23) or not (0 <= qh_end <= 23):
        issues.append(f"⚠️ Config: quiet_hours must be 0-23, got start={qh_start}, end={qh_end}")
        config["quiet_hours_start"] = max(0, min(23, qh_start))
        config["quiet_hours_end"] = max(0, min(23, qh_end))

    # Expected fleet hashrate must be positive (unless disabled by user)
    fleet_ths = config.get("expected_fleet_ths", 22.0)
    fleet_ths_disabled = config.get("expected_fleet_ths_disabled", False)
    if not isinstance(fleet_ths, (int, float)) or fleet_ths <= 0:
        # If user explicitly disabled this feature, use 1.0 as fallback and mark as disabled
        if fleet_ths_disabled:
            config["expected_fleet_ths"] = 1.0
            issues.append("ℹ️ Config: expected_fleet_ths feature disabled by user (using 1.0 fallback)")
        else:
            issues.append(f"⚠️ Config: expected_fleet_ths must be > 0, got {fleet_ths}")
            config["expected_fleet_ths"] = 22.0

    # Health warn threshold must be 0-100
    health_warn = config.get("health_warn_threshold", 70)
    if not isinstance(health_warn, (int, float)) or not (0 <= health_warn <= 100):
        issues.append(f"⚠️ Config: health_warn_threshold must be 0-100, got {health_warn}")
        config["health_warn_threshold"] = max(0, min(100, health_warn))

    # Power rate must be non-negative
    power_rate = config.get("power_rate_kwh", 0.12)
    if not isinstance(power_rate, (int, float)) or power_rate < 0:
        issues.append(f"⚠️ Config: power_rate_kwh must be >= 0, got {power_rate}")
        config["power_rate_kwh"] = 0.12

    # M-1 fix: Validate wallet addresses are not placeholders
    # This prevents operators from running with example config values
    placeholder_patterns = [
        "YOUR_", "PENDING_GENERATION", "CHANGE_ME", "PLACEHOLDER",
        "EXAMPLE_", "INSERT_", "PUT_YOUR_", "ENTER_YOUR_"
    ]

    # Check legacy single-coin wallet address
    # Skip this check if pool API is reachable - auto-detection will provide the real wallet
    wallet_addr = config.get("wallet_address", "")
    pool_api_available = False
    try:
        pool_url = config.get("pool_api_url", "http://localhost:4000")
        req = urllib.request.Request(f"{pool_url}/api/pools", method='GET')
        req.add_header('User-Agent', 'SpiralSentinel/1.0')
        with urllib.request.urlopen(req, timeout=3) as resp:
            if resp.status == 200:
                data = json.loads(resp.read().decode('utf-8'))
                pools = data.get("pools", [])
                if pools and pools[0].get("address"):
                    pool_api_available = True  # Pool API has real wallet, skip placeholder warning
    except Exception:
        pass  # Pool API not available, proceed with placeholder check

    if wallet_addr and not pool_api_available:
        for pattern in placeholder_patterns:
            if pattern.upper() in wallet_addr.upper():
                issues.append(f"🚨 CRITICAL: wallet_address appears to be a placeholder ('{wallet_addr}'). "
                              "Please set your actual wallet address.")
                break

    # Check multi-coin wallet addresses (skip if pool API provides real wallets)
    if not pool_api_available:
        coins = config.get("coins", [])
        for i, coin in enumerate(coins):
            if not coin.get("enabled", False):
                continue
            coin_wallet = coin.get("wallet_address", "")
            coin_name = coin.get("name", f"coins[{i}]")
            if coin_wallet:
                for pattern in placeholder_patterns:
                    if pattern.upper() in coin_wallet.upper():
                        issues.append(f"🚨 CRITICAL: {coin_name} wallet_address appears to be a placeholder ('{coin_wallet}'). "
                                      f"Please set your actual {coin_name} wallet address.")
                        break

    # SECURITY: Validate pool_api_url points to localhost or private network
    pool_url = config.get("pool_api_url", "http://localhost:4000")
    if pool_url:
        try:
            parsed = urlparse(pool_url)
            if parsed.scheme not in ("http", "https"):
                issues.append(f"🚨 CRITICAL: pool_api_url has invalid scheme '{parsed.scheme}' — must be http or https")
                config["pool_api_url"] = "http://localhost:4000"
            elif parsed.hostname:
                try:
                    ip = ipaddress.ip_address(parsed.hostname)
                    if not ip.is_private and not ip.is_loopback:
                        issues.append(f"⚠️ Config: pool_api_url points to non-private IP ({parsed.hostname}). "
                                      "This should point to localhost or a local network address.")
                except ValueError:
                    # Hostname, not IP — allow localhost only or warn
                    if parsed.hostname not in ("localhost", "127.0.0.1"):
                        issues.append(f"⚠️ Config: pool_api_url points to '{parsed.hostname}'. "
                                      "Verify this is correct — expected localhost or private IP.")
        except Exception:
            issues.append(f"⚠️ Config: pool_api_url is not a valid URL: {pool_url[:50]}")
            config["pool_api_url"] = "http://localhost:4000"

    # Also validate Discord webhook is not a placeholder
    discord_webhook = config.get("discord_webhook_url", "")
    if discord_webhook:
        for pattern in placeholder_patterns:
            if pattern.upper() in discord_webhook.upper():
                issues.append(f"⚠️ Config: discord_webhook appears to be a placeholder. "
                              "Please set your actual Discord webhook URL.")
                break

    # Print issues if any
    for issue in issues:
        logger.warning(issue)

    return issues


def load_config():
    # Directory already created at module level (with ProtectHome fallback)
    # Retry mkdir here as safety net in case module-level creation was deferred
    try:
        CONFIG_FILE.parent.mkdir(parents=True, exist_ok=True)
    except (PermissionError, OSError):
        pass  # Already handled at module level; config will use defaults if unwritable
    if CONFIG_FILE.exists():
        try:
            with open(CONFIG_FILE) as f:
                user_config = json.load(f)
            # SECURITY (Audit #3): Ensure config file permissions are restrictive on every load
            # Config may contain sensitive data like Discord webhook URLs and Telegram tokens
            try:
                os.chmod(CONFIG_FILE, 0o600)
            except (OSError, AttributeError):
                pass  # Best effort - may fail on Windows
            config = copy.deepcopy(DEFAULT_CONFIG)
            config.update(user_config)
            # Validate and auto-fix configuration
            issues = validate_config(config)
            if issues:
                logger.info(f"Config validation found {len(issues)} issue(s) - auto-corrected")
        except (json.JSONDecodeError, IOError, OSError) as e:
            logger.warning(f"Could not load config: {e}")
            config = copy.deepcopy(DEFAULT_CONFIG)
    else:
        try:
            _atomic_json_save(CONFIG_FILE, DEFAULT_CONFIG, indent=2)
            # SECURITY: Set config file permissions to 0600 (owner read/write only)
            # Config may contain sensitive data like Discord webhook URLs
            try:
                os.chmod(str(CONFIG_FILE), 0o600)
            except (OSError, AttributeError):
                pass  # Best effort - may fail on Windows
        except (PermissionError, OSError) as e:
            logger.warning(f"Could not write default config to {CONFIG_FILE}: {e}")
        config = copy.deepcopy(DEFAULT_CONFIG)

    # Environment variable overrides (for Docker deployment)
    # These take precedence over config file settings
    env_overrides = {
        "POOL_API_URL": "pool_api_url",
        "SPIRAL_ADMIN_API_KEY": "pool_admin_api_key",
        "DISCORD_WEBHOOK_URL": "discord_webhook_url",
        "TELEGRAM_BOT_TOKEN": "telegram_bot_token",
        "TELEGRAM_CHAT_ID": "telegram_chat_id",
        "XMPP_JID": "xmpp_jid",
        "XMPP_PASSWORD": "xmpp_password",
        "XMPP_RECIPIENT": "xmpp_recipient",
        "NTFY_URL": "ntfy_url",
        "NTFY_TOKEN": "ntfy_token",
        "SMTP_HOST": "smtp_host",
        "SMTP_PORT": "smtp_port",
        "SMTP_USERNAME": "smtp_username",
        "SMTP_PASSWORD": "smtp_password",
        "SMTP_FROM": "smtp_from",
        "SMTP_TO": "smtp_to",
        "EXPECTED_FLEET_THS": "expected_fleet_ths",
        "WALLET_ADDRESS": "wallet_address",
        "DGB_WALLET_ADDRESS": "wallet_address",  # Alias for multi-coin
        "ALERT_THEME": "alert_theme",
    }
    for env_var, config_key in env_overrides.items():
        env_value = os.environ.get(env_var)
        if env_value:
            # Convert numeric values
            if config_key == "expected_fleet_ths":
                try:
                    config[config_key] = float(env_value)
                except ValueError:
                    pass
            elif config_key == "smtp_port":
                try:
                    config[config_key] = int(env_value)
                except ValueError:
                    pass
            else:
                config[config_key] = env_value
            # AUDIT FIX (CR-1): Never log credential values — mask sensitive env overrides
            _sensitive_keys = {"pool_admin_api_key", "discord_webhook_url", "telegram_bot_token", "xmpp_password", "smtp_password", "ntfy_token"}
            if config_key in _sensitive_keys:
                logger.info(f"Config override from env: {config_key}=****")
            else:
                logger.info(f"Config override from env: {config_key}={env_value[:20]}..." if len(str(env_value)) > 20 else f"Config override from env: {config_key}={env_value}")

    # Fallback: Auto-discover admin API key from pool's config.yaml if not set
    # This mirrors dashboard.py:get_stratum_admin_api_key() fallback logic
    if not config.get("pool_admin_api_key"):
        stratum_config_path = INSTALL_DIR / "config" / "config.yaml"
        if stratum_config_path.exists():
            try:
                with open(stratum_config_path) as f:
                    for line in f:
                        line = line.strip()
                        # V1 format: adminApiKey: "value" or adminApiKey: value
                        if line.startswith("adminApiKey:"):
                            key = line.split(":", 1)[1].strip().strip('"').strip("'")
                            if key and len(key) >= 32:
                                config["pool_admin_api_key"] = key
                                logger.info("Auto-discovered pool_admin_api_key from stratum config.yaml")
                                break
                        # V2 format: admin_api_key: "value"
                        elif line.startswith("admin_api_key:"):
                            key = line.split(":", 1)[1].strip().strip('"').strip("'")
                            if key and len(key) >= 32:
                                config["pool_admin_api_key"] = key
                                logger.info("Auto-discovered pool_admin_api_key from stratum config.yaml")
                                break
            except (IOError, OSError) as e:
                logger.debug(f"Could not read stratum config for API key: {e}")

    # Fallback: Try to read Discord webhook from update-settings.conf if not set
    # This provides better UX - users configure webhook in one place
    if not config.get("discord_webhook_url"):
        update_settings_paths = [
            INSTALL_DIR / "config" / "update-settings.conf",
            DATA_DIR / "update-settings.conf",
        ]
        for settings_path in update_settings_paths:
            if settings_path.exists():
                try:
                    with open(settings_path) as f:
                        for line in f:
                            line = line.strip()
                            if line.startswith("DISCORD_WEBHOOK="):
                                webhook = line.split("=", 1)[1].strip().strip('"').strip("'")
                                if webhook and "YOUR" not in webhook:
                                    config["discord_webhook_url"] = webhook
                                    logger.info(f"Loaded Discord webhook from {settings_path}")
                                    break
                except (IOError, OSError) as e:
                    logger.debug(f"Could not read {settings_path}: {e}")
                if config.get("discord_webhook_url"):
                    break

    return config

CONFIG = load_config()
DISCORD_WEBHOOK_URL = CONFIG.get("discord_webhook_url", "")

# Hostname for alert footers — shows which server the alert came from.
# Configurable via "hostname_override" in config.json; defaults to OS hostname.
_SENTINEL_HOSTNAME = CONFIG.get("hostname_override", "") or socket.gethostname()

# ═══════════════════════════════════════════════════════════════════════════════
# MULTI-COIN HELPERS
# ═══════════════════════════════════════════════════════════════════════════════

MULTI_COIN_ENABLED = CONFIG.get("multi_coin_enabled", False)
CONFIGURED_COINS = CONFIG.get("coins", [])

# Global for auto-detected coin (set during startup or first call to get_enabled_coins)
AUTO_DETECTED_COIN = None

# Global startup time for alert suppression (set in main())
# During the suppression window, all alerts except critical ones are suppressed
SENTINEL_STARTUP_TIME = None
STARTUP_ALERT_SUPPRESSION_MINUTES = CONFIG.get("startup_alert_suppression_min", 30)

def get_enabled_coins():
    """Get list of enabled coins from config, with auto-detection fallback.

    Priority:
    1. If multi_coin_enabled=True, use configured coins list
    2. If single-coin mode with explicit config, use that
    3. Fall back to auto-detection from pool API
    """
    global AUTO_DETECTED_COIN

    if MULTI_COIN_ENABLED:
        return [c for c in CONFIGURED_COINS if c.get("enabled", False)]

    # Port mappings per coin (used for fallback)
    # NOTE: Pool IDs use _1 suffix to match stratum pool config (e.g., bc2_sha256_1)
    # Alphabetically ordered (no coin preference)
    COIN_PORT_DEFAULTS = {
        "BC2": {"stratum": 6333, "stratum_v2": 6334, "rpc": 8339, "zmq": 28338, "pool_id": "bc2_sha256_1"},
        "BCH": {"stratum": 5333, "stratum_v2": 5334, "rpc": 8432, "zmq": 28432, "pool_id": "bch_sha256_1"},
        "BTC": {"stratum": 4333, "stratum_v2": 4334, "rpc": 8332, "zmq": 28332, "pool_id": "btc_sha256_1"},
        "CAT": {"stratum": 12335, "stratum_v2": 12336, "rpc": 9932, "zmq": 28932, "pool_id": "cat_scrypt_1"},
        "DGB": {"stratum": 3333, "stratum_v2": 3334, "rpc": 14022, "zmq": 28532, "pool_id": "dgb_sha256_1"},
        "DGB-SCRYPT": {"stratum": 3336, "stratum_v2": 3337, "rpc": 14022, "zmq": 28532, "pool_id": "dgb_scrypt_1"},
        "DOGE": {"stratum": 8335, "stratum_v2": 8337, "rpc": 22555, "zmq": 28555, "pool_id": "doge_scrypt_1"},
        "FBTC": {"stratum": 18335, "stratum_v2": 18336, "rpc": 8340, "zmq": 28340, "pool_id": "fbtc_sha256_1"},
        "LTC": {"stratum": 7333, "stratum_v2": 7334, "rpc": 9332, "zmq": 28933, "pool_id": "ltc_scrypt_1"},
        "NMC": {"stratum": 14335, "stratum_v2": 14336, "rpc": 8336, "zmq": 28336, "pool_id": "nmc_sha256_1"},
        "PEP": {"stratum": 10335, "stratum_v2": 10336, "rpc": 33873, "zmq": 28873, "pool_id": "pep_scrypt_1"},
        "QBX": {"stratum": 20335, "stratum_v2": 20336, "rpc": 8344, "zmq": 28344, "pool_id": "qbx_sha256_1"},
        "SYS": {"stratum": 15335, "stratum_v2": 15336, "rpc": 8370, "zmq": 28370, "pool_id": "sys_sha256_1"},  # Merge-mining only (BTC parent) — cannot solo mine
        "XMY": {"stratum": 17335, "stratum_v2": 17336, "rpc": 10889, "zmq": 28889, "pool_id": "xmy_sha256_1"},
    }

    # Check if we have explicit single-coin config
    if CONFIG.get("wallet_address") and CONFIG.get("pool_id"):
        # User has configured Sentinel manually - use their config
        # Try to detect the correct coin symbol from pool
        detected = auto_detect_pool_coin() if AUTO_DETECTED_COIN is None else AUTO_DETECTED_COIN
        if detected:
            symbol = detected.get("symbol", "")
        else:
            # Try to infer coin from pool_id using prefix matching.
            # Order matters: check specific prefixes before generic ones
            # to avoid substring collisions (e.g., "fbtc" contains "btc",
            # "dgb_scrypt" contains "dgb").
            pool_id = CONFIG.get("pool_id", "").lower()
            _pool_id_prefixes = [
                ("dgb_scrypt", "DGB-SCRYPT"), ("dgb-scrypt", "DGB-SCRYPT"),
                ("fbtc", "FBTC"),
                ("btc", "BTC"), ("bch", "BCH"),
                ("bc2", "BC2"), ("bitcoinii", "BC2"),
                ("dgb", "DGB"),
                ("ltc", "LTC"), ("doge", "DOGE"), ("pep", "PEP"), ("cat", "CAT"),
                ("nmc", "NMC"), ("sys", "SYS"), ("xmy", "XMY"), ("qbx", "QBX"),
            ]
            symbol = ""
            for prefix, coin_sym in _pool_id_prefixes:
                if pool_id.startswith(prefix):
                    symbol = coin_sym
                    break

        if symbol and symbol in COIN_PORT_DEFAULTS:
            ports = COIN_PORT_DEFAULTS[symbol]
            return [{
                "symbol": symbol,
                "name": get_coin_name(symbol),
                "enabled": True,
                "pool_id": CONFIG.get("pool_id", ports["pool_id"]),
                "wallet_address": CONFIG.get("wallet_address", ""),
                "stratum_port": ports["stratum"],
                "stratum_v2_port": ports["stratum_v2"],
                "rpc_port": ports["rpc"],
                "zmq_port": ports["zmq"],
            }]

    # Try auto-detection from pool API
    if AUTO_DETECTED_COIN is None:
        AUTO_DETECTED_COIN = auto_detect_pool_coin()
        if AUTO_DETECTED_COIN:
            logger.info(f"Auto-detected pool coin: {AUTO_DETECTED_COIN['symbol']} ({AUTO_DETECTED_COIN['name']})")

    if AUTO_DETECTED_COIN:
        return [AUTO_DETECTED_COIN]

    # Cannot determine coin - return empty list (caller should handle this)
    logger.warning("Could not detect pool coin - no coins configured")
    return []

def get_coin_by_symbol(symbol):
    """Get coin config by symbol (DGB, BTC, BCH)"""
    for coin in get_enabled_coins():
        if coin.get("symbol", "").upper() == symbol.upper():
            return coin
    return None

def get_coin_emoji(symbol):
    """Get emoji for coin. Returns generic coin emoji if symbol is None."""
    if not symbol:
        return "🪙"
    emojis = {
        # SHA-256d coins
        "DGB": "💎",
        "BTC": "🟠",
        "BCH": "🟢",
        "BC2": "🔵",  # Bitcoin II - blue circle
        "NMC": "📛",  # Namecoin - name badge (DNS)
        "SYS": "⚙️",  # Syscoin - gear (platform)
        "XMY": "🌀",  # Myriad - spiral (multi-algo)
        "FBTC": "🔶",  # Fractal Bitcoin - orange diamond
        "QBX": "⚛️",   # Q-BitX - atom (post-quantum)
        # Scrypt coins
        "LTC": "🥈",  # Litecoin - silver
        "DOGE": "🐕",  # Dogecoin - doge
        "DGB-SCRYPT": "💎",  # Same as DGB (same blockchain)
        "PEP": "🐸",  # PepeCoin - frog
        "CAT": "🐱",  # Catcoin - cat
    }
    return emojis.get(symbol.upper(), "🪙")

def get_coin_name(symbol):
    """Get full name for coin. Returns 'Unknown' if symbol is None."""
    if not symbol:
        return "Unknown"
    names = {
        # SHA-256d coins
        "DGB": "DigiByte",
        "BTC": "Bitcoin",
        "BCH": "Bitcoin Cash",
        "BC2": "Bitcoin II",
        "NMC": "Namecoin",
        "SYS": "Syscoin",
        "XMY": "Myriad",
        "FBTC": "Fractal Bitcoin",
        "QBX": "Q-BitX",
        # Scrypt coins
        "LTC": "Litecoin",
        "DOGE": "Dogecoin",
        "DGB-SCRYPT": "DigiByte (Scrypt)",
        "PEP": "PepeCoin",
        "CAT": "Catcoin",
    }
    return names.get(symbol.upper(), symbol)

def get_coin_algorithm(symbol):
    """Get mining algorithm for a coin symbol.

    Returns:
        'scrypt' for Scrypt-based coins (LTC, DOGE, DGB-SCRYPT, PEP, CAT)
        'sha256d' for SHA-256d coins (DGB, BTC, BCH, BC2)
        'sha256d' (default) if symbol is None or unknown
    """
    if not symbol:
        return "sha256d"  # Default for unknown
    scrypt_coins = {"LTC", "LITECOIN", "DOGE", "DOGECOIN", "DGB-SCRYPT", "DIGIBYTE-SCRYPT",
                    "PEP", "PEPECOIN", "CAT", "CATCOIN"}
    return "scrypt" if symbol.upper() in scrypt_coins else "sha256d"

def is_scrypt_coin(symbol):
    """Check if a coin uses the Scrypt algorithm."""
    return get_coin_algorithm(symbol) == "scrypt"

def is_sha256d_coin(symbol):
    """Check if a coin uses the SHA-256d algorithm."""
    return get_coin_algorithm(symbol) == "sha256d"

# ═══════════════════════════════════════════════════════════════════════════════
# MERGE MINING (AuxPoW) CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════
# Merge mining allows mining multiple coins simultaneously using the same hashrate.
# The parent chain does the actual PoW, auxiliary chains accept proofs from the parent.
#
# Parent Chain -> Auxiliary Chains (AuxPoW)
# ─────────────────────────────────────────
# BTC  -> NMC, SYS, XMY (SHA-256d aux chains)
# LTC  -> DOGE (Scrypt aux chain)
# DOGE -> (none - DOGE is aux-only, cannot be parent)
#
# When mining a parent chain, you automatically mine all its aux chains too!
# The same hashrate contributes to block finding on ALL chains.

MERGE_MINING_PAIRS = {
    # SHA-256d parent chains and their auxiliary chains
    # NOTE: Must match coins.manifest.yaml - BTC/LTC are the only parent chains
    "BTC": ["NMC", "SYS", "XMY", "FBTC"],  # Bitcoin merge-mines: Namecoin, Syscoin, Myriad, Fractal
    "DGB": [],                                     # DigiByte SHA-256d - standalone (not a parent)
    "BCH": [],                                     # Bitcoin Cash - standalone (not a parent in manifest)
    "BC2": [],                                     # Bitcoin II - standalone
    # Scrypt parent chains and their auxiliary chains
    "LTC": ["DOGE", "PEP"],                       # Litecoin merge-mines: Dogecoin, PepeCoin
    "DGB-SCRYPT": [],                              # DigiByte Scrypt - standalone
}

# Reverse lookup: auxiliary chain -> parent chain(s)
AUX_TO_PARENT_CHAINS = {}
for parent, aux_list in MERGE_MINING_PAIRS.items():
    for aux in aux_list:
        if aux not in AUX_TO_PARENT_CHAINS:
            AUX_TO_PARENT_CHAINS[aux] = []
        AUX_TO_PARENT_CHAINS[aux].append(parent)

def is_merge_mineable(symbol):
    """Check if a coin supports merge mining (either as parent or auxiliary).

    Returns True if the coin can participate in merge mining.
    """
    if not symbol:
        return False
    symbol = symbol.upper()
    # Check if it's a parent chain with aux chains
    if symbol in MERGE_MINING_PAIRS and MERGE_MINING_PAIRS[symbol]:
        return True
    # Check if it's an auxiliary chain
    if symbol in AUX_TO_PARENT_CHAINS:
        return True
    return False

def is_parent_chain(symbol):
    """Check if a coin can be a parent chain for merge mining.

    Parent chains are the ones that do the actual PoW work.
    Mining a parent chain automatically mines its auxiliary chains.
    """
    if not symbol:
        return False
    symbol = symbol.upper()
    return symbol in MERGE_MINING_PAIRS and len(MERGE_MINING_PAIRS.get(symbol, [])) > 0

def is_aux_chain(symbol):
    """Check if a coin is an auxiliary (AuxPoW) chain.

    Auxiliary chains accept proofs from parent chains.
    They don't require dedicated mining - they're mined alongside the parent.
    """
    if not symbol:
        return False
    return symbol.upper() in AUX_TO_PARENT_CHAINS

def get_aux_chains(parent_symbol):
    """Get list of auxiliary chains that can be merge-mined with a parent chain.

    Args:
        parent_symbol: The parent chain symbol (e.g., "BTC", "LTC")

    Returns:
        List of auxiliary chain symbols (e.g., ["NMC", "SYS"])
    """
    if not parent_symbol:
        return []
    return MERGE_MINING_PAIRS.get(parent_symbol.upper(), [])

def get_parent_chains(aux_symbol):
    """Get list of parent chains that can merge-mine an auxiliary chain.

    Args:
        aux_symbol: The auxiliary chain symbol (e.g., "NMC", "DOGE")

    Returns:
        List of parent chain symbols (e.g., ["BTC", "BCH"])
    """
    if not aux_symbol:
        return []
    return AUX_TO_PARENT_CHAINS.get(aux_symbol.upper(), [])

def get_enabled_aux_chains(parent_symbol):
    """Get list of ENABLED auxiliary chains for a parent chain.

    Only returns aux chains that are actually configured and enabled in the pool.

    Args:
        parent_symbol: The parent chain symbol (e.g., "BTC")

    Returns:
        List of enabled auxiliary chain symbols
    """
    possible_aux = get_aux_chains(parent_symbol)
    if not possible_aux:
        return []

    enabled_symbols = {c.get("symbol", "").upper() for c in get_enabled_coins()}
    return [aux for aux in possible_aux if aux in enabled_symbols]

def get_merge_mining_summary(primary_coin):
    """Get a formatted summary of merge mining status for display.

    Args:
        primary_coin: The primary coin being mined

    Returns:
        Tuple of (is_merge_mining, summary_text, aux_coins_list)
    """
    if not primary_coin:
        return (False, "", [])

    primary_coin = primary_coin.upper()
    enabled_aux = get_enabled_aux_chains(primary_coin)

    if not enabled_aux:
        # Check if primary is an aux chain itself
        if is_aux_chain(primary_coin):
            parents = get_parent_chains(primary_coin)
            enabled_parents = [p for p in parents if p in {c.get("symbol", "").upper() for c in get_enabled_coins()}]
            if enabled_parents:
                return (True, f"🔗 Merge-mined via {', '.join(enabled_parents)}", [])
        return (False, "", [])

    # Primary is a parent chain with enabled aux chains
    aux_display = ", ".join([f"{get_coin_emoji(a)} {a}" for a in enabled_aux])
    return (True, f"🔗 +Merge: {aux_display}", enabled_aux)

# ═══════════════════════════════════════════════════════════════════════════════
# AUX CHAIN POOL DISCOVERY — For merge-mining block detection
# ═══════════════════════════════════════════════════════════════════════════════

_aux_pools_cache = {}  # Keyed by parent_symbol
_aux_pools_cache_time = {}  # Keyed by parent_symbol

def discover_active_aux_pools(parent_symbol):
    """Discover active auxiliary chain pools by querying the pool API.

    Queries /api/pools to find which aux chain pools are actually running.
    Results are cached per parent symbol for 5 minutes to avoid excessive API calls.

    Args:
        parent_symbol: The parent chain symbol (e.g., "BTC", "LTC")

    Returns:
        List of dicts: [{"symbol": "NMC", "pool_id": "nmc_sha256_1"}, ...]
    """
    global _aux_pools_cache, _aux_pools_cache_time

    if not parent_symbol or not is_parent_chain(parent_symbol):
        return []

    # Cache per parent symbol for 5 minutes
    cached = _aux_pools_cache.get(parent_symbol)
    cache_time = _aux_pools_cache_time.get(parent_symbol, 0)
    if cached is not None and (time.time() - cache_time) < 300:
        return cached

    possible_aux = get_aux_chains(parent_symbol)
    if not possible_aux:
        return []

    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        data = _http(f"{pool_url}/api/pools", timeout=10)
        if not data:
            return _aux_pools_cache.get(parent_symbol) or []

        pools = data.get("pools", []) if isinstance(data, dict) else data

        # Map API coin type names to symbols (API returns full names like "Namecoin")
        coin_type_to_symbol = {
            "BITCOIN": "BTC", "BITCOINCASH": "BCH", "BITCOINII": "BC2",
            "DIGIBYTE": "DGB", "LITECOIN": "LTC", "DOGECOIN": "DOGE",
            "PEPECOIN": "PEP", "CATCOIN": "CAT",
            "NAMECOIN": "NMC", "SYSCOIN": "SYS", "MYRIADCOIN": "XMY",
            "MYRIAD": "XMY", "FRACTALBITCOIN": "FBTC", "FRACTAL": "FBTC",
            "QBITX": "QBX", "Q-BITX": "QBX",
        }

        # Build symbol -> pool_id from API response
        active_pools = {}
        for pool in pools:
            coin_type = pool.get("coin", {}).get("type", "").upper()
            pool_id = pool.get("id", "")
            if coin_type and pool_id:
                symbol = coin_type_to_symbol.get(coin_type, coin_type)
                active_pools[symbol] = pool_id

        result = []
        for aux in possible_aux:
            if aux in active_pools:
                result.append({"symbol": aux, "pool_id": active_pools[aux]})

        _aux_pools_cache[parent_symbol] = result
        _aux_pools_cache_time[parent_symbol] = time.time()
        return result
    except Exception:
        return _aux_pools_cache.get(parent_symbol) or []


def format_hashrate(hashrate_hs, symbol=None, algorithm=None):
    """Format hashrate with algorithm-appropriate units.

    SHA-256d coins (ASICs): Typically TH/s to PH/s range
    Scrypt coins (ASICs): Typically MH/s to GH/s range (roughly 1000x slower)

    Args:
        hashrate_hs: Hashrate in H/s (hashes per second)
        symbol: Coin symbol (used to determine algorithm if algorithm not specified)
        algorithm: Explicitly specify 'sha256d' or 'scrypt'

    Returns:
        Formatted string like "1.23 TH/s" or "456.7 MH/s"
    """
    if hashrate_hs <= 0:
        return "0 H/s"

    # Determine algorithm from symbol if not explicitly provided
    if algorithm is None:
        if symbol:
            algorithm = get_coin_algorithm(symbol)
        else:
            algorithm = "sha256d"  # Default to SHA-256d

    # Define units for each algorithm (in descending order)
    if algorithm == "scrypt":
        # Scrypt: Modern ASICs like L7 are ~9 GH/s, network is ~1 PH/s
        units = [
            (1e15, "PH/s"),  # Petahash (network scale)
            (1e12, "TH/s"),  # Terahash (large pools)
            (1e9, "GH/s"),   # Gigahash (modern ASICs like L7)
            (1e6, "MH/s"),   # Megahash (typical ASIC range)
            (1e3, "KH/s"),   # Kilohash
            (1, "H/s"),
        ]
    else:
        # SHA-256d: Modern ASICs are 100+ TH/s, network is 500+ EH/s
        units = [
            (1e18, "EH/s"),  # Exahash (network scale)
            (1e15, "PH/s"),  # Petahash (large pools)
            (1e12, "TH/s"),  # Terahash (single ASICs)
            (1e9, "GH/s"),   # Gigahash
            (1e6, "MH/s"),   # Megahash
            (1e3, "KH/s"),   # Kilohash
            (1, "H/s"),
        ]

    for divisor, suffix in units:
        if hashrate_hs >= divisor:
            value = hashrate_hs / divisor
            # Use appropriate precision based on magnitude
            if value >= 100:
                return f"{value:.1f} {suffix}"
            elif value >= 10:
                return f"{value:.2f} {suffix}"
            else:
                return f"{value:.3f} {suffix}"

    return f"{hashrate_hs:.2f} H/s"

def format_hashrate_ths(hashrate_ths, symbol=None, algorithm=None):
    """Format hashrate given in TH/s (common for SHA-256d context).

    For Scrypt coins, converts to appropriate units (GH/s, MH/s).
    For SHA-256d coins, displays in TH/s or PH/s.

    Args:
        hashrate_ths: Hashrate in TH/s
        symbol: Coin symbol to determine algorithm
        algorithm: Explicitly specify algorithm

    Returns:
        Formatted string with appropriate units
    """
    # Convert TH/s to H/s and use the main formatter
    hashrate_hs = hashrate_ths * 1e12
    return format_hashrate(hashrate_hs, symbol=symbol, algorithm=algorithm)

def format_hashrate_ghs(hashrate_ghs, symbol=None, algorithm=None):
    """Format hashrate given in GH/s.

    Args:
        hashrate_ghs: Hashrate in GH/s
        symbol: Coin symbol to determine algorithm
        algorithm: Explicitly specify algorithm

    Returns:
        Formatted string with appropriate units
    """
    # Convert GH/s to H/s and use the main formatter
    hashrate_hs = hashrate_ghs * 1e9
    return format_hashrate(hashrate_hs, symbol=symbol, algorithm=algorithm)

def format_hashrate_phs(hashrate_phs, symbol=None, algorithm=None):
    """Format hashrate given in PH/s (network scale).

    Args:
        hashrate_phs: Hashrate in PH/s
        symbol: Coin symbol to determine algorithm
        algorithm: Explicitly specify algorithm

    Returns:
        Formatted string with appropriate units
    """
    # Convert PH/s to H/s and use the main formatter
    hashrate_hs = hashrate_phs * 1e15
    return format_hashrate(hashrate_hs, symbol=symbol, algorithm=algorithm)

def format_difficulty(difficulty):
    """Format network difficulty in human-readable format using SI prefixes.

    Uses SI prefixes (K, M, G, T, P) as miners expect, not naming conventions.

    Examples:
        38017052330 -> "38.0 G" (giga)
        1234567890123 -> "1.23 T" (tera)
        92233720368547 -> "92.2 T" (tera)
        123456789 -> "123.5 M" (mega)
        12345678 -> "12.3 M" (mega)
        1234567 -> "1.23 M" (mega)
        123456 -> "123,456" (comma separated)

    Args:
        difficulty: Network difficulty as a number

    Returns:
        Human-readable string like "1.68 G" or "1.23 T"
    """
    if difficulty is None or difficulty <= 0:
        return "0"

    # Define units in descending order (SI prefixes as miners expect)
    units = [
        (1e15, "P"),   # Peta
        (1e12, "T"),   # Tera
        (1e9, "G"),    # Giga
        (1e6, "M"),    # Mega
        (1e3, "K"),    # Kilo
    ]

    for divisor, suffix in units:
        if difficulty >= divisor:
            value = difficulty / divisor
            # Use appropriate precision
            if value >= 100:
                return f"{value:.1f} {suffix}"
            elif value >= 10:
                return f"{value:.2f} {suffix}"
            else:
                return f"{value:.3f} {suffix}"

    # Small numbers: use comma separator
    return f"{difficulty:,.0f}"

def get_primary_coin():
    """Get the primary (first enabled) coin symbol.
    Returns the first enabled coin's symbol, or None if no coins are configured.
    """
    coins = get_enabled_coins()
    if coins:
        if len(coins) > 1:
            logger.warning(f"get_primary_coin() called with {len(coins)} coins enabled - returning first coin arbitrarily: {coins[0].get('symbol', 'UNKNOWN')}")
        symbol = coins[0].get("symbol", "")
        return symbol.upper() if symbol else None
    return None

def get_primary_coin_config():
    """Get the full config for the primary coin.
    Returns None if no coins are configured.
    """
    coins = get_enabled_coins()
    if coins:
        if len(coins) > 1:
            logger.warning(f"get_primary_coin_config() called with {len(coins)} coins enabled - returning first coin arbitrarily: {coins[0].get('symbol', 'UNKNOWN')}")
        return coins[0]
    return None

def fetch_pool_stats_for_coin(coin):
    """Fetch pool stats for a specific coin.
    Uses /api/pools endpoint to get all pools, then finds the matching one.
    This avoids the /api/pools/{pool_id} endpoint which has regex validation issues
    with underscores in pool IDs like 'dgb_sha256_1'.
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = coin.get("pool_id", "")

        # Use /api/pools to get all pools (bypasses pool ID regex validation)
        url = f"{pool_url}/api/pools"
        data = _http(url, timeout=10)

        if data and isinstance(data, dict) and "pools" in data:
            # Find matching pool in the list
            for pool in data["pools"]:
                if pool.get("id") == pool_id:
                    return pool

        return None
    except (KeyError, TypeError):
        return None

def fetch_pool_stats_by_symbol(symbol):
    """Fetch pool stats for a coin by its symbol.

    This is a convenience wrapper that handles the case where the coin
    might not be in get_enabled_coins() (e.g., during startup or when
    querying a specific coin's network stats).

    Falls back to known pool_id patterns if coin config not found.
    """
    # Known pool_id patterns for each coin
    POOL_ID_PATTERNS = {
        "DGB": "dgb_sha256_1",
        "BTC": "btc_sha256_1",
        "BCH": "bch_sha256_1",
        "BC2": "bc2_sha256_1",
        "NMC": "nmc_sha256_1",
        "SYS": "sys_sha256_1",  # Merge-mining only (BTC parent required)
        "XMY": "xmy_sha256_1",
        "FBTC": "fbtc_sha256_1",
        "LTC": "ltc_scrypt_1",
        "DOGE": "doge_scrypt_1",
        "DGB-SCRYPT": "dgb_scrypt_1",
        "PEP": "pep_scrypt_1",
        "CAT": "cat_scrypt_1",
    }

    symbol = symbol.upper() if symbol else ""

    # Try getting coin config from enabled coins first
    coin_config = get_coin_by_symbol(symbol)
    if coin_config:
        return fetch_pool_stats_for_coin(coin_config)

    # Fallback: use known pool_id pattern
    pool_id = POOL_ID_PATTERNS.get(symbol)
    if pool_id:
        return fetch_pool_stats_for_coin({"pool_id": pool_id})

    return None

def fetch_pool_miners_for_coin(coin):
    """Fetch miner stats for a specific coin pool"""
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = coin.get("pool_id", "")
        url = f"{pool_url}/api/pools/{pool_id}/miners"
        data = _http(url, timeout=10)
        if data and isinstance(data, list):
            return {m.get("miner", ""): m for m in data}
        return {}
    except (KeyError, TypeError, AttributeError):
        return {}

def check_coin_node_synced(coin):
    """Check if a specific coin's node is synced"""
    try:
        rpc_port = coin.get("rpc_port")
        if not rpc_port:
            return False  # No RPC port configured
        req = urllib.request.Request(
            f"http://127.0.0.1:{rpc_port}",
            data=json.dumps({"method": "getblockchaininfo", "params": [], "id": 1}).encode(),
            headers={"Content-Type": "application/json"}
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            result = json.loads(resp.read().decode())
            if "result" in result:
                progress = result["result"].get("verificationprogress", 0)
                return progress >= 0.9999
        return False
    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError):
        return False

def get_all_coins_status():
    """Get sync status for all enabled coins"""
    statuses = {}
    for coin in get_enabled_coins():
        symbol = coin.get("symbol", "UNKNOWN")
        statuses[symbol] = {
            "synced": check_coin_node_synced(coin),
            "pool_stats": fetch_pool_stats_for_coin(coin),
            "name": coin.get("name", symbol),
        }
    return statuses

def fetch_all_pools():
    """Fetch all configured pools from the Spiral Pool API.

    Returns a list of all pools (enabled or not) with their stats.
    Used to show "Available Coins" section in Discord embeds.

    Returns:
        list: List of pool dicts with coin info, or empty list on error
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        req = urllib.request.Request(f"{pool_url}/api/pools")
        with urllib.request.urlopen(req, timeout=10) as response:
            if response.status != 200:
                return []
            data = json.loads(response.read().decode('utf-8'))
        return data.get("pools", [])
    except Exception as e:
        logger.debug(f"Could not fetch pools: {e}")
        return []


# ═══════════════════════════════════════════════════════════════════════════════
# DEVICE HINTS - Push device info to pool for ESP-Miner classification
# ═══════════════════════════════════════════════════════════════════════════════
# Device hints cache: tracks which miners we've pushed hints for
# Format: {ip: {"device_model": str, "asic_model": str, "asic_count": int, "hashrate_ghs": float, "pushed_at": float}}
_device_hints_cache = {}
_device_hints_push_interval = 3600  # Re-push hints every hour to handle pool restarts
_device_hints_no_key_warned = False  # Log missing API key warning only once

def push_device_hint_to_pool(ip, device_model, asic_model, asic_count, hashrate_ghs, algorithm=None):
    """Push device hint to pool for IP-based miner classification.

    This allows the pool to correctly classify ALL mining devices and set appropriate
    initial difficulty based on their hashrate, preventing low-difficulty share rejections.

    Supports all device types across all coins/algorithms:
    - SHA256: BitAxe, NerdQAxe++, Avalon, Antminer S19/S21, etc.
    - Scrypt: Antminer L7, Mini DOGE, Innosilicon A6+, etc.
    - Other algorithms: Whatsminer, Innosilicon, etc.

    The pool uses hashrate to calculate appropriate starting difficulty regardless of
    which coin is being mined. Difficulty is algorithm-specific, not coin-specific.

    Args:
        ip: Miner IP address
        device_model: Device model name (e.g., "NMAxe", "NerdQAxe++", "Avalon-Nano3s", "Antminer S19")
        asic_model: ASIC chip model (e.g., "BM1366", "BM1370", "BM1397")
        asic_count: Number of ASIC chips
        hashrate_ghs: Observed hashrate in GH/s
        algorithm: Mining algorithm (e.g., "sha256", "scrypt") - optional, for multi-algo pools
    """
    if not ip or not device_model:
        return False

    # Early exit if no API key configured — avoids N failed HTTP requests per cycle
    global _device_hints_no_key_warned
    admin_key = CONFIG.get("pool_admin_api_key", "")
    if not admin_key:
        if not _device_hints_no_key_warned:
            logger.info("Device hint push skipped: pool_admin_api_key not configured (this message appears once)")
            _device_hints_no_key_warned = True
        return False

    # Check cache to avoid spamming the pool API
    cached = _device_hints_cache.get(ip)
    now = time.time()
    if cached:
        # Re-push if: device info changed, hashrate changed significantly, or interval elapsed
        # Hashrate change detection: >10% difference triggers re-push (handles mode changes)
        cached_hr = cached.get("hashrate_ghs", 0) or 0
        current_hr = hashrate_ghs or 0
        hashrate_changed = False
        if cached_hr > 0 and current_hr > 0:
            hashrate_diff_pct = abs(current_hr - cached_hr) / cached_hr * 100
            hashrate_changed = hashrate_diff_pct > 10  # >10% change triggers re-push
        elif cached_hr == 0 and current_hr > 0:
            hashrate_changed = True  # New hashrate data available

        if (cached.get("device_model") == device_model and
            cached.get("asic_model") == asic_model and
            not hashrate_changed and
            (now - cached.get("pushed_at", 0)) < _device_hints_push_interval):
            return True  # Already pushed recently, no significant changes

        # Log hashrate change detection
        if hashrate_changed:
            logger.info(f"Hashrate change detected for {ip}: {cached_hr:.0f} -> {current_hr:.0f} GH/s, re-pushing device hint")

    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        admin_key = CONFIG.get("pool_admin_api_key", "")

        # Build the device hint payload
        hint_data = {
            "ip": ip,
            "deviceModel": device_model,
            "asicModel": asic_model or "",
            "asicCount": asic_count or 0,
            "hashrateGHs": hashrate_ghs or 0,
        }
        if algorithm:
            hint_data["algorithm"] = algorithm

        payload = json.dumps(hint_data).encode('utf-8')

        headers = {
            "User-Agent": f"SpiralSentinel/{__version__}",
            "Content-Type": "application/json",
        }
        if admin_key:
            headers["X-API-Key"] = admin_key

        req = urllib.request.Request(
            f"{pool_url}/api/admin/device-hints",
            data=payload,
            headers=headers,
            method="POST"
        )

        with urllib.request.urlopen(req, timeout=10) as response:
            if response.status in [200, 201]:
                # Update cache
                _device_hints_cache[ip] = {
                    "device_model": device_model,
                    "asic_model": asic_model,
                    "asic_count": asic_count,
                    "hashrate_ghs": hashrate_ghs,
                    "pushed_at": now,
                }
                logger.debug(f"Pushed device hint: {ip} -> {device_model} ({asic_model})")
                return True
        return False
    except urllib.error.HTTPError as e:
        if e.code == 401 or e.code == 403:
            logger.warning(f"Device hint push failed: API key required (set pool_admin_api_key in config)")
        else:
            logger.debug(f"Device hint push failed for {ip}: HTTP {e.code}")
        return False
    except Exception as e:
        logger.debug(f"Device hint push failed for {ip}: {e}")
        return False


def push_all_device_hints():
    """Push device hints for all configured miners.

    Called periodically to ensure pool has up-to-date device info,
    especially after pool restarts which clear the in-memory registry.

    Device hints help the pool correctly classify miners and set appropriate
    initial difficulty, preventing low-difficulty share rejections.

    Supports:
    - ESP-Miner/AxeOS devices (NMAxe, NerdQAxe++, BitAxe) via HTTP API
    - Avalon/CGMiner devices via CGMiner API port 4028
    - Antminer, Whatsminer, Innosilicon via CGMiner API
    """
    pushed = 0

    # Push hints for ESP-Miner/AxeOS devices (HTTP API)
    # These devices all report "ESP-Miner" as user-agent but have different hardware
    # Includes: axeos (generic), nmaxe, nerdqaxe (SHA256), hammer (Scrypt)
    for miner_type in ["axeos", "nmaxe", "nerdqaxe", "hammer"]:
        for m in MINERS.get(miner_type, []):
            ip = m.get("ip")
            if not ip:
                continue
            # Fetch device info from miner's HTTP API
            info = fetch_device_info(ip)
            if info:
                device_model = info.get("device_model")
                if device_model:  # Only push if we got a valid model
                    if push_device_hint_to_pool(
                        ip,
                        device_model,
                        info.get("asic_model"),
                        info.get("asic_count"),
                        info.get("hashrate_ghs")
                    ):
                        pushed += 1
                        logger.debug(f"Device hint pushed: {ip} -> {device_model}")

    # Push hints for CGMiner-based miners (Avalon, Antminer, Whatsminer, Innosilicon)
    # These need device hints for proper difficulty routing across all coins
    # Format: (miner_type, fetch_func, model_prefix, algorithm)
    cgminer_types = [
        ("avalon", fetch_avalon, "Avalon", "sha256"),
        ("antminer", fetch_antminer, "Antminer", "sha256"),  # S19/S21 are SHA256
        ("antminer_scrypt", fetch_antminer, "Antminer-L", "scrypt"),  # L7/L9 are Scrypt
        ("whatsminer", fetch_whatsminer, "Whatsminer", "sha256"),
        ("innosilicon", fetch_innosilicon, "Innosilicon", "sha256"),
        ("futurebit", fetch_futurebit, "FutureBit", "sha256"),  # Apollo/Apollo II (SHA256)
        ("goldshell", fetch_goldshell, "Goldshell", "scrypt"),  # Mini DOGE/LT5/LT6 are Scrypt
    ]
    for miner_type, fetch_func, model_prefix, algorithm in cgminer_types:
        for m in MINERS.get(miner_type, []):
            ip = m.get("ip")
            if not ip:
                continue
            port = m.get("port", 4028)
            # Allow per-miner algorithm override in config
            miner_algo = m.get("algorithm", algorithm)
            try:
                data = fetch_func(ip, port)
                if data and data.get("hashrate_ghs"):
                    # Prefer API-detected model (e.g., "Avalon Nano3s" from CGMiner stats)
                    # Fall back to config name with model prefix for pool classification
                    api_model = data.get("device_model", "")
                    if api_model:
                        device_model = api_model
                    else:
                        configured_name = m.get("name", ip.split(".")[-1])
                        if model_prefix.lower() in configured_name.lower():
                            device_model = configured_name
                        else:
                            device_model = f"{model_prefix} {configured_name}"
                    hashrate_ghs = data.get("hashrate_ghs", 0)
                    if push_device_hint_to_pool(
                        ip,
                        device_model,
                        "",  # CGMiner doesn't expose ASIC model
                        0,   # CGMiner doesn't expose ASIC count
                        hashrate_ghs,
                        algorithm=miner_algo
                    ):
                        pushed += 1
                        logger.debug(f"Device hint pushed: {ip} -> {device_model} ({hashrate_ghs:.0f} GH/s, {miner_algo})")
            except Exception as e:
                logger.debug(f"Failed to fetch {miner_type} at {ip}: {e}")

    if pushed > 0:
        logger.info(f"Pushed {pushed} device hints to pool")
    return pushed


def fetch_device_info(ip, timeout=5):
    """Fetch device identification info from a miner's HTTP API.

    Returns device model, ASIC model, chip count, and hashrate for
    ESP-Miner/AxeOS-based devices (NMAxe, NerdQAxe++, BitAxe, etc.)

    Args:
        ip: Miner IP address
        timeout: Request timeout in seconds

    Returns:
        dict with device_model, asic_model, asic_count, hashrate_ghs or None on failure
    """
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        return None
    try:
        req = urllib.request.Request(
            f"http://{ip}/api/system/info",
            headers={"User-Agent": f"SpiralSentinel/{__version__}"}
        )
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode(errors='ignore')
            # Handle potentially malformed JSON (some firmware versions)
            if raw.rfind('}') >= 0:
                raw = raw[:raw.rfind('}')+1]
            d = json.loads(raw)

            # Extract device model - different firmware versions use different keys
            device_model = (
                d.get("deviceModel") or
                d.get("boardVersion") or
                d.get("board") or
                d.get("model") or
                ""
            )

            return {
                "device_model": device_model,
                "asic_model": d.get("ASICModel") or d.get("asicModel") or "",
                "asic_count": d.get("asicCount") or d.get("asic_count") or 1,
                "hashrate_ghs": d.get("hashRate") or d.get("hashrate") or 0,
            }
    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, json.JSONDecodeError, OSError, ValueError):
        return None


def auto_detect_pool_coin():
    """Auto-detect the active coin from the pool API.

    This queries the Spiral Pool API to determine which coin(s) are configured,
    allowing Sentinel to automatically adapt without manual configuration.

    Returns:
        dict: Detected coin configuration or None if detection fails
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        req = urllib.request.Request(f"{pool_url}/api/pools")
        with urllib.request.urlopen(req, timeout=10) as response:
            if response.status != 200:
                return None
            data = json.loads(response.read().decode('utf-8'))
        pools = data.get("pools", [])
        if not pools:
            return None

        # Handle multi-coin case
        if len(pools) > 1:
            logger.warning(f"auto_detect_pool_coin() found {len(pools)} pools - multi-coin mode detected, cannot auto-detect single coin")
            return None

        # Extract coin info from first pool (V1 single-coin mode)
        pool = pools[0]
        coin_info = pool.get("coin", {})
        coin_type = coin_info.get("type", "").lower()
        pool_id = pool.get("id", "")
        pool_address = pool.get("address", "")

        # Map coin type to symbol
        # Note: Using exact key matching (dict lookup), so order doesn't matter
        coin_map = {
            # SHA-256d coins
            "digibyte": "DGB",
            "digibyte-sha256": "DGB",
            "dgb": "DGB",
            "bitcoincash": "BCH",
            "bitcoin-cash": "BCH",
            "bch": "BCH",
            "bitcoinii": "BC2",
            "bitcoin-ii": "BC2",
            "bitcoin2": "BC2",
            "bc2": "BC2",
            "bcii": "BC2",
            "bitcoin": "BTC",
            "btc": "BTC",
            # SHA-256d merge-mineable coins
            "namecoin": "NMC",
            "nmc": "NMC",
            "syscoin": "SYS",
            "sys": "SYS",
            "myriad": "XMY",
            "myriadcoin": "XMY",
            "xmy": "XMY",
            "fractalbitcoin": "FBTC",
            "fractal": "FBTC",
            "fbtc": "FBTC",
            "qbitx": "QBX",
            "q-bitx": "QBX",
            "qbx": "QBX",
            # Scrypt coins
            "litecoin": "LTC",
            "ltc": "LTC",
            "dogecoin": "DOGE",
            "doge": "DOGE",
            "digibyte-scrypt": "DGB-SCRYPT",
            "dgb-scrypt": "DGB-SCRYPT",
            "pepecoin": "PEP",
            "pep": "PEP",
            "meme": "PEP",
            "catcoin": "CAT",
            "cat": "CAT",
        }

        symbol = coin_map.get(coin_type)
        if not symbol:
            # Unknown coin type - don't assume DGB, return None to trigger fallback
            logger.warning(f"Unknown coin type from API: {coin_type}")
            return None

        # Default port mappings per coin (used as fallback if API doesn't provide ports)
        default_ports = {
            # SHA-256d coins
            "DGB": {"stratum": 3333, "stratum_v2": 3334, "rpc": 14022, "zmq": 28532},
            "BTC": {"stratum": 4333, "stratum_v2": 4334, "rpc": 8332, "zmq": 28332},
            "BCH": {"stratum": 5333, "stratum_v2": 5334, "rpc": 8432, "zmq": 28432},
            "BC2": {"stratum": 6333, "stratum_v2": 6334, "rpc": 8339, "zmq": 28338},
            # SHA-256d merge-mineable coins
            "NMC": {"stratum": 14335, "stratum_v2": 14336, "rpc": 8336, "zmq": 28336},
            "SYS": {"stratum": 15335, "stratum_v2": 15336, "rpc": 8370, "zmq": 28370},
            "XMY": {"stratum": 17335, "stratum_v2": 17336, "rpc": 10889, "zmq": 28889},
            "FBTC": {"stratum": 18335, "stratum_v2": 18336, "rpc": 8340, "zmq": 28340},
            # Scrypt coins
            "LTC": {"stratum": 7333, "stratum_v2": 7334, "rpc": 9332, "zmq": 28933},
            "DOGE": {"stratum": 8335, "stratum_v2": 8337, "rpc": 22555, "zmq": 28555},
            "DGB-SCRYPT": {"stratum": 3336, "stratum_v2": 3337, "rpc": 14022, "zmq": 28532},
            "PEP": {"stratum": 10335, "stratum_v2": 10336, "rpc": 33873, "zmq": 28873},
            "CAT": {"stratum": 12335, "stratum_v2": 12336, "rpc": 9932, "zmq": 28932},
        }
        defaults = default_ports.get(symbol, {})  # No fallback - use empty dict if symbol not found

        # Try to get actual ports from API response
        # Pool ports section contains the actual configured ports
        ports_section = pool.get("ports", {})
        stratum_port = ports_section.get("stratum") or pool.get("stratumPort") or defaults.get("stratum", 0)
        stratum_v2_port = ports_section.get("stratumV2") or pool.get("stratumV2Port") or defaults.get("stratum_v2", 0)

        # Daemon section contains RPC and ZMQ ports
        daemon_section = pool.get("daemon", {})
        rpc_port = daemon_section.get("port") or daemon_section.get("rpcPort") or defaults.get("rpc", 0)
        zmq_port = daemon_section.get("zmqPort") or defaults.get("zmq", 0)

        return {
            "symbol": symbol,
            "name": get_coin_name(symbol),
            "enabled": True,
            "pool_id": pool_id,
            "wallet_address": pool_address,
            "stratum_port": stratum_port,
            "stratum_v2_port": stratum_v2_port,
            "rpc_port": rpc_port,
            "zmq_port": zmq_port,
            "detected": True,  # Flag to indicate auto-detection
        }
    except (urllib.error.URLError, json.JSONDecodeError, KeyError, TypeError, OSError):
        return None

# Track last detected coin for change detection
LAST_DETECTED_COIN = None
COIN_CHECK_INTERVAL = 900  # Check for coin changes every 15 minutes (rare event)

# ═══════════════════════════════════════════════════════════════════════════════
# MULTI-COIN MODE TRACKING
# ═══════════════════════════════════════════════════════════════════════════════
# Track whether pool is in solo (1 coin) or multi (2+ coins) mode
# Detect mode switches and alert on coin additions/removals
# Solo mode: exactly 1 coin enabled (any of the 12 supported coins)
# Multi mode: 2-12 coins enabled simultaneously

LAST_DETECTED_MODE = None  # "solo" or "multi"
LAST_DETECTED_COINS = []   # List of coin symbols that were active (1-12 coins)
MODE_CHECK_INTERVAL = 300  # Check mode every 5 minutes (300 seconds)
_last_mode_check = 0

def check_for_coin_change():
    """Check if the pool's configured coin has changed.

    This is called periodically to detect when the operator switches
    coins using pool-mode.sh. Returns change info if detected.

    Returns:
        dict: {"old": old_symbol, "new": new_symbol, "config": new_config} or None
    """
    global LAST_DETECTED_COIN, AUTO_DETECTED_COIN

    current = auto_detect_pool_coin()
    if current is None:
        return None

    current_symbol = current.get("symbol", "").upper()

    # First run - just store the initial coin
    if LAST_DETECTED_COIN is None:
        LAST_DETECTED_COIN = current_symbol
        return None

    # Check if coin changed
    if current_symbol != LAST_DETECTED_COIN:
        old_coin = LAST_DETECTED_COIN
        LAST_DETECTED_COIN = current_symbol
        AUTO_DETECTED_COIN = current  # Update global auto-detected coin

        return {
            "old": old_coin,
            "new": current_symbol,
            "config": current,
        }

    return None

def create_coin_change_embed(old_coin, new_coin, new_config=None):
    """Create Discord embed for coin change notification.

    Args:
        old_coin: Previous coin symbol
        new_coin: New coin symbol
        new_config: Optional coin configuration dict with actual port info
    """
    old_emoji = get_coin_emoji(old_coin)
    new_emoji = get_coin_emoji(new_coin)
    old_name = get_coin_name(old_coin)
    new_name = get_coin_name(new_coin)

    # Get port info from config if available, otherwise use defaults
    if new_config:
        ports = {
            "stratum": new_config.get("stratum_port", "unknown"),
            "stratum_v2": new_config.get("stratum_v2_port", "unknown")
        }
    else:
        # Fallback defaults for all 14 supported coins (only used if no config available)
        default_ports = {
            # SHA-256d coins
            "DGB": {"stratum": 3333, "stratum_v2": 3334},
            "BTC": {"stratum": 4333, "stratum_v2": 4334},
            "BCH": {"stratum": 5333, "stratum_v2": 5334},
            "BC2": {"stratum": 6333, "stratum_v2": 6334},
            # SHA-256d merge-mineable coins
            "NMC": {"stratum": 14335, "stratum_v2": 14336},
            "SYS": {"stratum": 15335, "stratum_v2": 15336},
            "XMY": {"stratum": 17335, "stratum_v2": 17336},
            "FBTC": {"stratum": 18335, "stratum_v2": 18336},
            # Scrypt coins
            "LTC": {"stratum": 7333, "stratum_v2": 7334},
            "DOGE": {"stratum": 8335, "stratum_v2": 8337},
            "DGB-SCRYPT": {"stratum": 3336, "stratum_v2": 3337},
            "PEP": {"stratum": 10335, "stratum_v2": 10336},
            "CAT": {"stratum": 12335, "stratum_v2": 12336},
        }
        ports = default_ports.get(new_coin, {"stratum": "unknown", "stratum_v2": "unknown"})

    desc = f"{theme('coin.change.body')}\n\n"
    desc += f"**{old_name}** → **{new_name}** ({new_coin})"

    fields = [
        {"name": f"{old_emoji} Previous", "value": f"{old_name} ({old_coin})", "inline": True},
        {"name": f"{new_emoji} Current", "value": f"{new_name} ({new_coin})", "inline": True},
        {"name": "🔌 Stratum Ports", "value": f"V1: `{ports['stratum']}`\nV2: `{ports['stratum_v2']}`", "inline": True},
    ]

    return _embed(
        theme("coin.change.title", old=old_coin, new=new_coin),
        desc,
        COLORS.get("yellow", 0xFFD700),
        fields,
        footer=theme("coin.change.footer", time=local_now().strftime('%Y-%m-%d %H:%M:%S'))
    )

def handle_coin_change(change_info, state):
    """Handle a detected coin change.

    This is called when the pool switches coins. It:
    1. Sends a Discord notification
    2. Updates internal state
    3. Logs the change

    Args:
        change_info: dict with old, new, and config keys
        state: SentinelState instance
    """
    old_coin = change_info["old"]
    new_coin = change_info["new"]
    new_config = change_info["config"]

    logger.info("=" * 60)
    logger.info("COIN CHANGE DETECTED!")
    logger.info(f"   {get_coin_emoji(old_coin)} {old_coin} ({get_coin_name(old_coin)})")
    logger.info(f"   ↓")
    logger.info(f"   {get_coin_emoji(new_coin)} {new_coin} ({get_coin_name(new_coin)})")
    logger.info("=" * 60)

    # Send Discord notification (pass config for accurate port info)
    embed = create_coin_change_embed(old_coin, new_coin, new_config)
    send_alert("coin_change", embed, state)

    # Log to state for tracking
    if hasattr(state, 'coin_changes'):
        state.coin_changes.append({
            "timestamp": local_now().isoformat(),
            "from": old_coin,
            "to": new_coin,
        })
        state.coin_changes = state.coin_changes[-100:]

    logger.info(f"Sentinel now monitoring {new_coin} ({get_coin_name(new_coin)})")


def check_dashboard_setup_complete():
    """Check if the Spiral Dash setup wizard has been completed.

    Reads the dashboard config to check the 'first_run' flag.
    When first_run is True, the dashboard is still in setup mode.
    When first_run is False, setup has been completed.

    Returns:
        bool: True if setup is complete (first_run=False), False otherwise
    """
    dashboard_configs = [
        INSTALL_DIR / "dashboard" / "data" / "dashboard_config.json",  # Production
        SHARED_DATA_DIR / "dashboard_config.json",                      # Shared data dir
    ]

    for cfg_path in dashboard_configs:
        if cfg_path.exists():
            try:
                with open(cfg_path) as f:
                    cfg = json.load(f)
                    first_run = cfg.get("first_run", True)
                    logger.debug(f"Dashboard config found at {cfg_path}, first_run={first_run}")
                    return not first_run  # Setup complete when first_run is False
            except (json.JSONDecodeError, IOError, OSError) as e:
                logger.debug(f"Could not read dashboard config at {cfg_path}: {e}")
                continue

    # No dashboard config found - assume setup not complete
    logger.debug("No dashboard config found - assuming setup not complete")
    return False


def detect_pool_mode():
    """Detect current pool mode (solo vs multi) from dashboard API.

    Queries the dashboard's /api/config/server-mode endpoint to determine
    the current pool configuration.

    Returns:
        dict: {
            "mode": "solo" | "multi",
            "coins": ["DGB", ...],  # List of active coin symbols
            "coins_config": [...],  # Full config for each coin
        } or None if detection fails
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        # Try the dashboard API first (port 1618 by default)
        dashboard_url = pool_url.replace(":4000", ":1618")
        url = f"{dashboard_url}/api/config/server-mode"
        # Pass dashboard API key if configured (auth may be required)
        dash_headers = {}
        dash_api_key = CONFIG.get("dashboard_api_key", "")
        if dash_api_key:
            dash_headers["X-API-Key"] = dash_api_key
        data = _http(url, timeout=10, headers=dash_headers if dash_headers else None)

        if data and isinstance(data, dict):
            mode = data.get("detected_mode", "solo")
            coins = data.get("detected_coins", [])
            coins_config = data.get("coins_config", [])

            return {
                "mode": mode,
                "coins": coins,
                "coins_config": coins_config,
            }

        # Fallback: Query pool API for pools list
        url = f"{pool_url}/api/pools"
        data = _http(url, timeout=10)
        if data and isinstance(data, dict):
            pools = data.get("pools", [])
            coins = []
            # Exact-match lookup — consistent with auto_detect_pool_coin().
            # The old substring approach ("cat" in coin_type) could match
            # unrelated strings and caused spurious notifications for coins
            # the user never installed.
            _coin_type_map = {
                # SHA-256d coins
                "bitcoincash": "BCH", "bitcoin-cash": "BCH", "bch": "BCH",
                "bitcoinii": "BC2", "bitcoin-ii": "BC2", "bitcoin2": "BC2", "bc2": "BC2", "bcii": "BC2",
                "fractalbitcoin": "FBTC", "fractal-bitcoin": "FBTC", "fractal": "FBTC", "fbtc": "FBTC",
                "bitcoin": "BTC", "btc": "BTC",
                # Scrypt coins
                "dgb-scrypt": "DGB-SCRYPT", "dgb_scrypt": "DGB-SCRYPT", "digibyte-scrypt": "DGB-SCRYPT",
                "digibyte": "DGB", "dgb": "DGB",
                "litecoin": "LTC", "ltc": "LTC",
                "dogecoin": "DOGE", "doge": "DOGE",
                "pepecoin": "PEP", "pep": "PEP",
                "catcoin": "CAT", "cat": "CAT",
                # SHA-256d merge-mineable aux coins
                "namecoin": "NMC", "nmc": "NMC",
                "syscoin": "SYS", "sys": "SYS",
                "myriad": "XMY", "myriadcoin": "XMY", "xmy": "XMY",
            }
            for pool in pools:
                coin_info = pool.get("coin", {})
                coin_type = coin_info.get("type", "").lower()
                symbol = _coin_type_map.get(coin_type)
                if symbol:
                    coins.append(symbol)
                elif coin_type:
                    logger.debug(f"detect_pool_mode: unknown coin type '{coin_type}' from pool API")

            return {
                "mode": "multi" if len(coins) > 1 else "solo",
                "coins": coins,
                "coins_config": [],
            }

        return None
    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError, KeyError, TypeError):
        return None


def check_for_mode_change(state=None):
    """Check if pool mode or active coins have changed.

    Detects:
    - Mode switch: solo -> multi or multi -> solo
    - Coin added: New coin enabled in multi-coin mode
    - Coin removed: Coin disabled in multi-coin mode

    Returns:
        dict: Change info or None if no change detected
    """
    global LAST_DETECTED_MODE, LAST_DETECTED_COINS, _last_mode_check

    current_time = time.time()
    if current_time - _last_mode_check < MODE_CHECK_INTERVAL:
        return None
    _last_mode_check = current_time

    pool_mode = detect_pool_mode()
    if pool_mode is None:
        return None

    current_mode = pool_mode["mode"]
    current_coins = sorted(pool_mode["coins"])

    # First run - initialize state
    if LAST_DETECTED_MODE is None:
        LAST_DETECTED_MODE = current_mode
        LAST_DETECTED_COINS = current_coins
        logger.info(f"Initial mode detected: {current_mode.upper()} with coins: {', '.join(current_coins)}")
        return None

    changes = []

    # Check for mode switch
    if current_mode != LAST_DETECTED_MODE:
        changes.append({
            "type": "mode_switch",
            "old_mode": LAST_DETECTED_MODE,
            "new_mode": current_mode,
            "coins": current_coins,
        })

    # Check for coin additions
    added_coins = [c for c in current_coins if c not in LAST_DETECTED_COINS]
    for coin in added_coins:
        changes.append({
            "type": "coin_added",
            "coin": coin,
            "mode": current_mode,
        })

    # Check for coin removals
    removed_coins = [c for c in LAST_DETECTED_COINS if c not in current_coins]
    for coin in removed_coins:
        changes.append({
            "type": "coin_removed",
            "coin": coin,
            "mode": current_mode,
        })

    # Update tracked state
    old_mode = LAST_DETECTED_MODE
    old_coins = LAST_DETECTED_COINS.copy()
    LAST_DETECTED_MODE = current_mode
    LAST_DETECTED_COINS = current_coins

    if changes:
        return {
            "changes": changes,
            "old_mode": old_mode,
            "new_mode": current_mode,
            "old_coins": old_coins,
            "new_coins": current_coins,
            "coins_config": pool_mode.get("coins_config", []),
        }

    return None


def create_mode_switch_embed(old_mode, new_mode, old_coins, new_coins):
    """Create Discord embed for pool mode switch alert."""
    if new_mode == "multi":
        title = theme("mode_switch.solo_to_multi")
        desc = f"Pool is now mining **{len(new_coins)} coins** simultaneously.\n"
        desc += f"Active coins: {', '.join([f'{get_coin_emoji(c)} {c}' for c in new_coins])}"
        color = COLORS.get("cyan", 0x00FFFF)
    else:
        # Assertion: Multi→Solo switch must result in exactly 1 coin
        if len(new_coins) != 1:
            logger.warning(f"Multi→Solo switch resulted in {len(new_coins)} coins, expected exactly 1")
        kept_coin = new_coins[0] if new_coins else "UNKNOWN"
        title = theme("mode_switch.multi_to_solo", coin=kept_coin)
        desc = f"Pool switched from multi-coin to **SOLO {get_coin_name(kept_coin)}** mining.\n"
        desc += f"Removed coins: {', '.join([f'{get_coin_emoji(c)} {c}' for c in old_coins if c not in new_coins])}"
        color = COLORS.get("yellow", 0xFFFF00)

    fields = [
        {"name": "Previous Mode", "value": old_mode.upper(), "inline": True},
        {"name": "New Mode", "value": new_mode.upper(), "inline": True},
        {"name": "Active Coins", "value": "\n".join([f"{get_coin_emoji(c)} {get_coin_name(c)}" for c in new_coins]) or "None", "inline": True},
    ]

    return _embed(title, desc, color, fields,
                  footer=theme("mode_switch.footer", time=local_now().strftime('%Y-%m-%d %H:%M:%S')))


def create_coin_added_embed(coin, mode, coins_config=None):
    """Create Discord embed for coin addition alert."""
    coin_emoji = get_coin_emoji(coin)
    coin_name = get_coin_name(coin)

    title = theme("coin.added.title", coin_emoji=coin_emoji, coin=coin)
    desc = f"**{coin_name}** {theme('coin.added.body')}\n"
    desc += f"Pool is now in **{mode.upper()}** mode."

    fields = [
        {"name": "Coin", "value": f"{coin_emoji} {coin_name}", "inline": True},
        {"name": "Mode", "value": mode.upper(), "inline": True},
    ]

    # Add stratum port info if available
    if coins_config:
        for cfg in coins_config:
            if cfg.get("symbol", "").upper() == coin.upper():
                port = cfg.get("stratum_port", "unknown")
                fields.append({"name": "Stratum Port", "value": str(port), "inline": True})
                break

    return _embed(title, desc, COLORS.get("green", 0x00FF41), fields,
                  footer=theme("coin.added.footer", time=local_now().strftime('%Y-%m-%d %H:%M:%S')))


def create_coin_removed_embed(coin, mode):
    """Create Discord embed for coin removal alert."""
    coin_emoji = get_coin_emoji(coin)
    coin_name = get_coin_name(coin)

    title = theme("coin.removed.title", coin_emoji=coin_emoji, coin=coin)
    desc = f"**{coin_name}** {theme('coin.removed.body')}\n"
    desc += f"Pool is now in **{mode.upper()}** mode."

    fields = [
        {"name": "Coin", "value": f"{coin_emoji} {coin_name}", "inline": True},
        {"name": "Mode", "value": mode.upper(), "inline": True},
    ]

    return _embed(title, desc, COLORS.get("orange", 0xFF6B35), fields,
                  footer=theme("coin.removed.footer", time=local_now().strftime('%Y-%m-%d %H:%M:%S')))


def create_consolidated_coin_config_embed(change_info):
    """Create a single consolidated embed for all coin/mode configuration changes.

    Instead of sending separate alerts for mode_switch, coin_added, coin_removed,
    this creates one comprehensive summary of all changes.
    """
    changes = change_info.get("changes", [])
    old_mode = change_info["old_mode"]
    new_mode = change_info["new_mode"]
    old_coins = change_info["old_coins"]
    new_coins = change_info["new_coins"]

    # Determine change types
    has_mode_switch = any(c["type"] == "mode_switch" for c in changes)
    added_coins = [c["coin"] for c in changes if c["type"] == "coin_added"]
    removed_coins = [c["coin"] for c in changes if c["type"] == "coin_removed"]

    # Build title based on changes
    if has_mode_switch:
        title = theme("coin_config.mode_switch", old=old_mode.upper(), new=new_mode.upper())
        color = COLORS.get("cyan", 0x00FFFF)
    elif added_coins and removed_coins:
        title = theme("coin_config.coins_changed")
        color = COLORS.get("yellow", 0xFFFF00)
    elif added_coins:
        title = theme("coin_config.coins_added", count=len(added_coins), plural="S" if len(added_coins) > 1 else "")
        color = COLORS.get("green", 0x00FF00)
    else:
        title = theme("coin_config.coins_removed", count=len(removed_coins), plural="S" if len(removed_coins) > 1 else "")
        color = COLORS.get("orange", 0xFF6B35)

    # Build description
    desc_parts = []
    if has_mode_switch:
        desc_parts.append(f"Pool mode changed from **{old_mode.upper()}** to **{new_mode.upper()}**")
    if added_coins:
        coins_str = ", ".join([f"{get_coin_emoji(c)} {c}" for c in added_coins])
        desc_parts.append(f"Added: {coins_str}")
    if removed_coins:
        coins_str = ", ".join([f"{get_coin_emoji(c)} {c}" for c in removed_coins])
        desc_parts.append(f"Removed: {coins_str}")

    desc = "\n".join(desc_parts)

    # Build fields
    fields = []

    # Active coins field
    active_coins_str = "\n".join([f"{get_coin_emoji(c)} {get_coin_name(c)}" for c in new_coins]) or "None"
    fields.append({"name": "Active Coins", "value": active_coins_str, "inline": True})

    # Mode field
    fields.append({"name": "Mode", "value": new_mode.upper(), "inline": True})

    # Changes summary field
    changes_summary = []
    if has_mode_switch:
        changes_summary.append(f"🔄 Mode: {old_mode} → {new_mode}")
    for coin in added_coins:
        changes_summary.append(f"➕ {get_coin_emoji(coin)} {coin}")
    for coin in removed_coins:
        changes_summary.append(f"➖ {get_coin_emoji(coin)} {coin}")
    if changes_summary:
        fields.append({"name": "Changes", "value": "\n".join(changes_summary), "inline": True})

    return _embed(title, desc, color, fields,
                  footer=theme("coin_config.footer", time=local_now().strftime('%Y-%m-%d %H:%M:%S')))


def handle_mode_changes(change_info, state):
    """Process and alert on mode/coin changes.

    CONSOLIDATED: Sends a single alert summarizing all changes instead of
    separate alerts for each mode_switch, coin_added, and coin_removed event.

    Args:
        change_info: Dict with changes, old_mode, new_mode, old_coins, new_coins
        state: MonitorState instance
    """
    if not change_info or not change_info.get("changes"):
        return

    changes = change_info["changes"]
    old_mode = change_info["old_mode"]
    new_mode = change_info["new_mode"]
    old_coins = change_info["old_coins"]
    new_coins = change_info["new_coins"]
    coins_config = change_info.get("coins_config", [])

    logger.info("=" * 60)
    logger.info("POOL MODE/COIN CHANGE DETECTED!")
    logger.info(f"   Mode: {old_mode.upper()} → {new_mode.upper()}")
    logger.info(f"   Coins: {', '.join(old_coins)} → {', '.join(new_coins)}")
    logger.info("=" * 60)

    # Log individual changes for debugging
    for change in changes:
        change_type = change["type"]
        if change_type == "mode_switch":
            logger.info(f"  ↳ Mode switch: {old_mode} → {new_mode}")
        elif change_type == "coin_added":
            logger.info(f"  ↳ Coin added: {get_coin_emoji(change['coin'])} {change['coin']}")
        elif change_type == "coin_removed":
            logger.info(f"  ↳ Coin removed: {get_coin_emoji(change['coin'])} {change['coin']}")

    # Send a SINGLE consolidated alert instead of multiple individual alerts
    embed = create_consolidated_coin_config_embed(change_info)

    # Use "coin_config_change" as the alert type (respects quiet hours)
    send_alert("coin_config_change", embed, state)

    # Track in state
    if hasattr(state, 'mode_changes'):
        state.mode_changes.append({
            "timestamp": local_now().isoformat(),
            "old_mode": old_mode,
            "new_mode": new_mode,
            "old_coins": old_coins,
            "new_coins": new_coins,
        })
        state.mode_changes = state.mode_changes[-100:]


# ═══════════════════════════════════════════════════════════════════════════════
# PER-COIN HEALTH MONITORING
# ═══════════════════════════════════════════════════════════════════════════════
# Monitor each coin's node status, sync progress, and hashrate

COIN_HEALTH_CHECK_INTERVAL = 300  # Check coin health every 5 minutes
_last_coin_health_check = 0
_coin_health_state = {}  # Track per-coin health status


def check_coin_health(coin_symbol, coin_config=None):
    """Check health status for a specific coin.

    Checks:
    - Node connectivity (RPC reachable)
    - Sync status (blocks behind)
    - Pool hashrate (significant drops)

    Returns:
        dict: Health status with issues list
    """
    issues = []
    status = {
        "symbol": coin_symbol,
        "node_up": False,
        "sync_progress": 0,
        "blocks_behind": 0,
        "pool_hashrate": 0,
        "issues": issues,
    }

    # Get coin config if not provided
    if coin_config is None:
        coin_config = get_coin_by_symbol(coin_symbol)
    if not coin_config:
        issues.append({"type": "config_missing", "message": f"No config for {coin_symbol}"})
        return status

    # Check node connectivity via pool API
    try:
        pool_stats = fetch_pool_stats_for_coin(coin_config)
        if pool_stats:
            status["node_up"] = True
            pool_stats_inner = pool_stats.get("poolStats", {})
            status["pool_hashrate"] = pool_stats_inner.get("poolHashrate", 0)

            # Track block height for monitoring
            block_height = pool_stats_inner.get("blockHeight", 0)
            status["block_height"] = block_height

            # Note: Sync-behind detection requires an external block height reference.
            # The API's blockHeight is from the local node — if the node is behind,
            # it simply reports a lower height with nothing to compare against.
            # blocks_behind and sync_progress remain at their defaults (0).
        else:
            issues.append({
                "type": "node_down",
                "message": f"{coin_symbol} node unreachable or pool not responding",
            })
    except Exception as e:
        issues.append({
            "type": "node_error",
            "message": f"Error checking {coin_symbol} node",
        })

    return status


def check_all_coins_health():
    """Check health of all enabled coins.

    Returns:
        dict: {coin_symbol: health_status, ...}
    """
    global _last_coin_health_check
    current_time = time.time()

    if current_time - _last_coin_health_check < COIN_HEALTH_CHECK_INTERVAL:
        return _coin_health_state.copy()

    _last_coin_health_check = current_time
    results = {}

    for coin in get_enabled_coins():
        symbol = coin.get("symbol", "UNKNOWN").upper()
        results[symbol] = check_coin_health(symbol, coin)

    return results


def create_coin_node_down_embed(coin_symbol, coin_name):
    """Create Discord embed for coin node down alert."""
    coin_emoji = get_coin_emoji(coin_symbol)
    ts = local_now().strftime('%Y-%m-%d %H:%M:%S')

    desc = f"""```diff
- {theme("coin.node_down.banner", coin_name=coin_name)}
```

{coin_emoji} **{coin_name}** {theme("coin.node_down.body")}
Mining for this coin may be interrupted.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {"name": "Coin", "value": f"{coin_emoji} {coin_name}", "inline": True},
        {"name": "Status", "value": "❌ OFFLINE", "inline": True},
        {"name": "🔧 Action", "value": f"`sudo systemctl restart {coin_name.lower().replace(' ', '')}d`", "inline": False},
    ]

    return _embed(theme("coin.node_down.title", coin_emoji=coin_emoji, coin=coin_symbol), desc, COLORS["red"], fields,
                  footer=f"🌀 Spiral Sentinel v{__version__} • {theme('coin.node_down.footer', time=ts)}")


def create_coin_sync_behind_embed(coin_symbol, coin_name, blocks_behind):
    """Create Discord embed for coin sync behind alert."""
    coin_emoji = get_coin_emoji(coin_symbol)
    ts = local_now().strftime('%Y-%m-%d %H:%M:%S')

    desc = f"""```fix
{theme("coin.sync_behind.banner", coin_name=coin_name)} — {blocks_behind:,} blocks behind
```

{coin_emoji} **{coin_name}** node is **{blocks_behind:,} blocks** behind the network.
{theme("coin.sync_behind.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {"name": "Coin", "value": f"{coin_emoji} {coin_name}", "inline": True},
        {"name": "Blocks Behind", "value": f"⏳ `{blocks_behind:,}`", "inline": True},
        {"name": "Status", "value": "🔄 Syncing...", "inline": True},
    ]

    return _embed(theme("coin.sync_behind.title", coin_emoji=coin_emoji, coin=coin_symbol), desc, COLORS["yellow"], fields,
                  footer=f"🌀 Spiral Sentinel v{__version__} • {theme('coin.sync_behind.footer', time=ts)}")


def create_coin_node_recovered_embed(coin_symbol, coin_name):
    """Create Discord embed for coin node recovery alert."""
    coin_emoji = get_coin_emoji(coin_symbol)
    ts = local_now().strftime('%Y-%m-%d %H:%M:%S')

    desc = f"""```diff
+ {theme("coin.recovered.banner", coin_name=coin_name)}
```

{coin_emoji} **{coin_name}** {theme("coin.recovered.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {"name": "Coin", "value": f"{coin_emoji} {coin_name}", "inline": True},
        {"name": "Status", "value": "✅ ONLINE", "inline": True},
    ]

    return _embed(theme("coin.recovered.title", coin_emoji=coin_emoji, coin=coin_symbol), desc, COLORS["green"], fields,
                  footer=f"🌀 Spiral Sentinel v{__version__} • {theme('coin.recovered.footer', time=ts)}")


def handle_coin_health_alerts(health_results, state):
    """Process coin health results and send alerts for issues.

    Tracks state to avoid duplicate alerts.
    """
    global _coin_health_state

    for symbol, health in health_results.items():
        prev_health = _coin_health_state.get(symbol, {})
        issues = health.get("issues", [])

        for issue in issues:
            issue_type = issue.get("type")

            # Check if this is a new issue (not previously alerted)
            prev_issues = [i.get("type") for i in prev_health.get("issues", [])]

            if issue_type == "node_down" and "node_down" not in prev_issues:
                embed = create_coin_node_down_embed(symbol, get_coin_name(symbol))
                send_alert("coin_node_down", embed, state)

            elif issue_type == "sync_behind" and "sync_behind" not in prev_issues:
                blocks_behind = issue.get("blocks_behind", 0)
                embed = create_coin_sync_behind_embed(symbol, get_coin_name(symbol), blocks_behind)
                send_alert("coin_sync_behind", embed, state)

        # Check for recovery (node came back online)
        if health.get("node_up") and not prev_health.get("node_up", True):
            embed = create_coin_node_recovered_embed(symbol, get_coin_name(symbol))
            send_alert("miner_online", embed, state)  # Reuse miner_online cooldown category
            logger.info(f"{symbol} node recovered — alert sent")

    # Update state
    _coin_health_state = health_results.copy()


# ═══════════════════════════════════════════════════════════════════════════════
# STUCK SYNC DETECTION
# ═══════════════════════════════════════════════════════════════════════════════
# Alert when a coin's blockchain hasn't advanced for STUCK_SYNC_THRESHOLD_SECS
# while still in initial sync.  Catches bad peers, network failures, and hung
# daemons that would otherwise be silent for hours on a fresh node.

STUCK_SYNC_THRESHOLD_SECS = 1800   # 30 min with no block progress = stuck
STUCK_SYNC_ALERT_COOLDOWN_SECS = 3600  # Re-alert at most once per hour per coin

_sync_progress_history = {}
# Per coin: {"blocks": int, "last_change": float, "alerted_at": float, "progress": float}

_last_known_difficulty = {}
# Per coin symbol: last observed network difficulty (float)

_dry_streak_tracking = {}
# Per coin symbol: {"last_block_time": float, "alerted_at": float}

_stratum_down_since = None
# float timestamp when pool API first became unreachable, or None when healthy
_stratum_down_alerted = False
# True after the initial stratum_down alert has fired (avoids repeat alerts per outage)


def _get_coin_sync_state(coin_config):
    """Return (blocks, progress) from getblockchaininfo for a coin, or (None, None) on error."""
    rpc_port = coin_config.get("rpc_port")
    if not rpc_port:
        return None, None
    try:
        req = urllib.request.Request(
            f"http://127.0.0.1:{rpc_port}",
            data=json.dumps({"method": "getblockchaininfo", "params": [], "id": 1}).encode(),
            headers={"Content-Type": "application/json"},
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            result = json.loads(resp.read().decode())
            r = result.get("result", {})
            return r.get("blocks", 0), r.get("verificationprogress", 0.0)
    except Exception:
        return None, None


def create_stuck_sync_embed(coin_symbol, coin_name, blocks, progress_pct, stuck_minutes):
    """Discord embed for a coin whose initial sync has made no progress."""
    coin_emoji = get_coin_emoji(coin_symbol)
    ts = local_now().strftime('%Y-%m-%d %H:%M:%S')

    desc = f"""```diff
- {coin_symbol} BLOCKCHAIN SYNC STALLED
```

{coin_emoji} **{coin_name}** initial sync has made **no progress for {stuck_minutes} minutes**.

**Current block:** `{blocks:,}`
**Sync progress:** `{progress_pct:.1f}%`

This may indicate a network issue, bad peers, or a hung daemon.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
**Suggested actions:**
• Check peers: `{coin_symbol.lower()}-cli getpeerinfo | grep synced`
• Restart daemon: `spiralctl node restart {coin_symbol}`
• Check logs: `journalctl -fu {coin_symbol.lower()}d --no-pager | tail -40`"""

    fields = [
        {"name": "Coin", "value": f"{coin_emoji} {coin_name}", "inline": True},
        {"name": "Progress", "value": f"`{progress_pct:.1f}%`", "inline": True},
        {"name": "Stalled", "value": f"`{stuck_minutes}m`", "inline": True},
    ]

    return _embed(
        f"⏸️ {coin_symbol} Sync Stalled",
        desc, COLORS["yellow"], fields,
        footer=f"🌀 Spiral Sentinel v{__version__} • Detected at {ts}",
    )


def check_stuck_syncs(state):
    """Check all enabled coins for stalled initial syncs and alert if stuck."""
    now = time.time()
    for coin in get_enabled_coins():
        symbol = coin.get("symbol", "UNKNOWN").upper()
        blocks, progress = _get_coin_sync_state(coin)

        # Skip coins we can't reach or that are already fully synced
        if blocks is None or progress is None:
            continue
        if progress >= 0.9999:
            _sync_progress_history.pop(symbol, None)
            continue

        hist = _sync_progress_history.get(symbol)
        if hist is None:
            # First observation — record baseline, don't alert yet
            _sync_progress_history[symbol] = {
                "blocks": blocks, "last_change": now,
                "alerted_at": 0.0, "progress": progress,
            }
            continue

        if blocks > hist["blocks"]:
            # Progress made — reset the clock
            hist["blocks"] = blocks
            hist["last_change"] = now
            hist["progress"] = progress
            continue

        # No progress — check if we've been stuck long enough to alert
        stuck_secs = now - hist["last_change"]
        if stuck_secs < STUCK_SYNC_THRESHOLD_SECS:
            continue

        # Respect cooldown — don't spam
        if now - hist.get("alerted_at", 0.0) < STUCK_SYNC_ALERT_COOLDOWN_SECS:
            continue

        stuck_minutes = int(stuck_secs // 60)
        progress_pct = progress * 100
        coin_name = get_coin_name(symbol)
        embed = create_stuck_sync_embed(symbol, coin_name, blocks, progress_pct, stuck_minutes)
        send_alert("coin_sync_behind", embed, state)
        hist["alerted_at"] = now
        logger.warning(f"Stuck sync alert: {symbol} at block {blocks:,} ({progress_pct:.1f}%) — no progress for {stuck_minutes}m")


# ═══════════════════════════════════════════════════════════════════════════════
# DRY STREAK ALERTING — no block found for N × expected interval
# ═══════════════════════════════════════════════════════════════════════════════

def create_dry_streak_embed(coin_symbol, coin_name, elapsed_hrs, expected_hrs, multiplier):
    """Discord embed for a coin that hasn't found a block in an unusually long time."""
    coin_emoji = get_coin_emoji(coin_symbol)
    ts = local_now().strftime('%Y-%m-%d %H:%M:%S')
    desc = (
        f"```diff\n- {coin_symbol} DRY STREAK DETECTED\n```\n"
        f"{coin_emoji} **{coin_name}** has not found a block in **{elapsed_hrs:.1f} hours**.\n\n"
        f"Expected interval: ~**{expected_hrs:.1f}h** · Current streak: **{elapsed_hrs:.1f}h** "
        f"(**{elapsed_hrs/expected_hrs:.1f}×** expected)\n\n"
        f"This is statistical variance — keep mining! Your odds reset with every share."
    )
    fields = [
        {"name": "Coin", "value": f"{coin_emoji} {coin_name}", "inline": True},
        {"name": "Elapsed", "value": f"`{elapsed_hrs:.1f}h`", "inline": True},
        {"name": "Threshold", "value": f"`{multiplier}× expected`", "inline": True},
    ]
    return _embed(
        f"🌵 {coin_symbol} Dry Streak",
        desc, COLORS["yellow"], fields,
        footer=f"🌀 Spiral Sentinel v{__version__} • {ts}",
    )


def check_dry_streak(state):
    """Alert when a coin has gone significantly longer than expected without finding a block."""
    if not CONFIG.get("dry_streak_enabled", True):
        return
    multiplier = CONFIG.get("dry_streak_multiplier", 3)
    now = time.time()
    enabled_coins = get_enabled_coins()

    for coin in enabled_coins:
        symbol = coin.get("symbol", "UNKNOWN").upper()
        try:
            pool_data = fetch_pool_stats_for_coin(coin)
            if not pool_data:
                continue

            # Get pool hashrate (H/s) and network difficulty
            pool_stats = pool_data.get("poolStats", {})
            pool_hashrate_hps = pool_stats.get("poolHashrate", 0)
            network_diff = pool_data.get("networkDifficulty", 0)

            if pool_hashrate_hps <= 0 or network_diff <= 0:
                continue

            # Expected time to block = (difficulty * 2^32) / pool_hashrate
            expected_secs = (network_diff * (2 ** 32)) / pool_hashrate_hps
            expected_hrs = expected_secs / 3600

            # Determine last block time: prefer state.block_history per coin, else pool API
            last_block_t = None
            for entry in reversed(state.block_history):
                if entry.get("coin", "").upper() == symbol:
                    last_block_t = entry["t"]
                    break

            if last_block_t is None:
                # Try pool API lastPoolBlockTime (Unix timestamp)
                last_pool_block = pool_stats.get("lastPoolBlockTime") or pool_data.get("lastPoolBlockTime")
                if last_pool_block:
                    try:
                        import datetime as _dt
                        if isinstance(last_pool_block, str):
                            # ISO format
                            lb_dt = _dt.datetime.fromisoformat(last_pool_block.replace("Z", "+00:00"))
                            last_block_t = lb_dt.timestamp()
                        elif isinstance(last_pool_block, (int, float)):
                            last_block_t = float(last_pool_block)
                    except Exception:
                        pass

            # Track initial observation per coin
            tracking = _dry_streak_tracking.get(symbol)
            if last_block_t is None:
                # No block data yet — record sentinel start time as baseline
                if tracking is None:
                    _dry_streak_tracking[symbol] = {"last_block_time": now, "alerted_at": 0.0}
                continue

            # Update last known block time if we have newer data
            if tracking is None:
                _dry_streak_tracking[symbol] = {"last_block_time": last_block_t, "alerted_at": 0.0}
                tracking = _dry_streak_tracking[symbol]
            elif last_block_t > tracking["last_block_time"]:
                tracking["last_block_time"] = last_block_t
                tracking["alerted_at"] = 0.0  # Reset alert when a new block is found

            elapsed_secs = now - tracking["last_block_time"]
            threshold_secs = expected_secs * multiplier

            if elapsed_secs < threshold_secs:
                continue

            # Check cooldown
            cooldown = ALERT_COOLDOWNS.get("dry_streak", 21600)
            if now - tracking.get("alerted_at", 0.0) < cooldown:
                continue

            elapsed_hrs = elapsed_secs / 3600
            coin_name = get_coin_name(symbol)
            embed = create_dry_streak_embed(symbol, coin_name, elapsed_hrs, expected_hrs, multiplier)
            # Pass state=None: cooldown is tracked per-coin via _dry_streak_tracking above,
            # so we bypass the generic rate limiter which would block subsequent coins.
            send_alert("dry_streak", embed, None)
            tracking["alerted_at"] = now
            logger.warning(f"Dry streak alert: {symbol} — {elapsed_hrs:.1f}h without a block ({multiplier}× threshold)")

        except Exception as e:
            logger.warning(f"Dry streak check error ({symbol}): {e}")


# ═══════════════════════════════════════════════════════════════════════════════
# NETWORK DIFFICULTY CHANGE ALERTS
# ═══════════════════════════════════════════════════════════════════════════════

def create_difficulty_change_embed(coin_symbol, coin_name, old_diff, new_diff, pct_change):
    """Discord embed for a significant network difficulty change."""
    coin_emoji = get_coin_emoji(coin_symbol)
    ts = local_now().strftime('%Y-%m-%d %H:%M:%S')
    direction = "increased" if pct_change > 0 else "decreased"
    direction_emoji = "📈" if pct_change > 0 else "📉"
    impact = "harder to find blocks" if pct_change > 0 else "easier to find blocks — odds improved"
    desc = (
        f"{direction_emoji} **{coin_name}** network difficulty has {direction} by "
        f"**{abs(pct_change):.1f}%**.\n\n"
        f"Mining is now {impact}."
    )
    fields = [
        {"name": "Previous", "value": f"`{format_difficulty(old_diff)}`", "inline": True},
        {"name": "New", "value": f"`{format_difficulty(new_diff)}`", "inline": True},
        {"name": "Change", "value": f"`{pct_change:+.1f}%`", "inline": True},
    ]
    return _embed(
        f"{direction_emoji} {coin_symbol} Difficulty {direction.title()}",
        desc, COLORS["yellow"] if pct_change > 0 else COLORS["green"], fields,
        footer=f"🌀 Spiral Sentinel v{__version__} • {ts}",
    )


def check_difficulty_changes(state):
    """Alert when network difficulty changes significantly for any enabled coin."""
    if not CONFIG.get("difficulty_alert_enabled", True):
        return
    threshold_pct = CONFIG.get("difficulty_alert_threshold_pct", 25)
    now = time.time()

    for coin in get_enabled_coins():
        symbol = coin.get("symbol", "UNKNOWN").upper()
        try:
            pool_data = fetch_pool_stats_for_coin(coin)
            if not pool_data:
                continue
            network_diff = pool_data.get("networkDifficulty", 0)
            if network_diff <= 0:
                continue

            old_diff = _last_known_difficulty.get(symbol)
            if old_diff is None:
                # First observation — store baseline, no alert
                _last_known_difficulty[symbol] = network_diff
                continue

            pct_change = ((network_diff - old_diff) / old_diff) * 100

            if abs(pct_change) < threshold_pct:
                continue

            alert_key = f"difficulty_change:{symbol}"
            cooldown = ALERT_COOLDOWNS.get("difficulty_change", 3600)
            if now - state.last_alerts.get(alert_key, 0) < cooldown:
                continue

            coin_name = get_coin_name(symbol)
            embed = create_difficulty_change_embed(symbol, coin_name, old_diff, network_diff, pct_change)
            # Pass state=None: cooldown is tracked per-coin above (alert_key includes symbol),
            # so we bypass the generic rate limiter which uses a single key and would
            # block alerts for subsequent coins in the same monitoring cycle.
            send_alert("difficulty_change", embed, None)
            state.last_alerts[alert_key] = now
            # Update baseline only when alert fires — prevents tick-to-tick comparison spam
            _last_known_difficulty[symbol] = network_diff
            logger.info(f"Difficulty change alert: {symbol} {pct_change:+.1f}% "
                        f"({format_difficulty(old_diff)} → {format_difficulty(network_diff)})")

        except Exception as e:
            logger.warning(f"Difficulty change check error ({symbol}): {e}")


# ═══════════════════════════════════════════════════════════════════════════════
# DISK SPACE MONITORING
# ═══════════════════════════════════════════════════════════════════════════════

def create_disk_alert_embed(path, used_pct, used_gb, total_gb, level):
    """Discord embed for disk space warning or critical alert."""
    ts = local_now().strftime('%Y-%m-%d %H:%M:%S')
    color = COLORS["red"] if level == "critical" else COLORS["yellow"]
    emoji = "🚨" if level == "critical" else "⚠️"
    desc = (
        f"{emoji} Disk space on **`{path}`** is at **{used_pct:.1f}%**.\n\n"
        f"Used: **{used_gb:.1f} GB** / **{total_gb:.1f} GB**\n\n"
        + ("**CRITICAL**: Free space is dangerously low. Pool may stop accepting shares "
           "if the database or chain data cannot grow." if level == "critical" else
           "Consider cleaning up old logs or chain data before this becomes critical.")
    )
    fields = [
        {"name": "Mount", "value": f"`{path}`", "inline": True},
        {"name": "Used", "value": f"`{used_pct:.1f}%`", "inline": True},
        {"name": "Free", "value": f"`{total_gb - used_gb:.1f} GB`", "inline": True},
    ]
    return _embed(
        f"{emoji} Disk Space {level.title()}: {path}",
        desc, color, fields,
        footer=f"🌀 Spiral Sentinel v{__version__} • {ts}",
    )


def check_disk_space(state):
    """Check monitored mount points for disk space warnings."""
    if not CONFIG.get("disk_monitor_enabled", True):
        return
    warn_pct = CONFIG.get("disk_warn_pct", 85)
    critical_pct = CONFIG.get("disk_critical_pct", 95)
    monitor_paths = CONFIG.get("disk_monitor_paths", ["/", "/spiralpool", "/var"])
    now = time.time()

    for path in monitor_paths:
        try:
            st = os.statvfs(path)
        except (OSError, AttributeError):
            continue  # Path doesn't exist or OS doesn't support statvfs

        total_bytes = st.f_frsize * st.f_blocks
        free_bytes = st.f_frsize * st.f_bavail
        used_bytes = total_bytes - free_bytes
        if total_bytes <= 0:
            continue

        used_pct = (used_bytes / total_bytes) * 100
        used_gb = used_bytes / (1024 ** 3)
        total_gb = total_bytes / (1024 ** 3)

        level = None
        alert_key = None
        if used_pct >= critical_pct:
            level = "critical"
            alert_key = f"disk_critical:{path}"
            cooldown = ALERT_COOLDOWNS.get("disk_critical", 300)
        elif used_pct >= warn_pct:
            level = "warning"
            alert_key = f"disk_warning:{path}"
            cooldown = ALERT_COOLDOWNS.get("disk_warning", 3600)

        if level is None:
            continue

        if now - state.last_alerts.get(alert_key, 0) < cooldown:
            continue

        embed = create_disk_alert_embed(path, used_pct, used_gb, total_gb, level)
        alert_type = "disk_critical" if level == "critical" else "disk_warning"
        # Pass state=None: cooldown is tracked per-path above, so we must bypass
        # the generic rate limiter in send_alert which uses a single non-path key
        # and would block alerts for subsequent paths in the same monitoring cycle.
        send_alert(alert_type, embed, None)
        state.last_alerts[alert_key] = now
        logger.warning(f"Disk space {level}: {path} at {used_pct:.1f}% ({used_gb:.1f}/{total_gb:.1f} GB)")


# ═══════════════════════════════════════════════════════════════════════════════
# MEMPOOL CONGESTION ALERT (BTC)
# ═══════════════════════════════════════════════════════════════════════════════

def _get_btc_mempool_info():
    """Fetch Bitcoin mempool info via direct JSON-RPC (no auth required on localhost)."""
    btc_coin = get_coin_by_symbol("BTC")
    if not btc_coin:
        return None
    rpc_port = btc_coin.get("rpc_port", 8332)
    try:
        req = urllib.request.Request(
            f"http://127.0.0.1:{rpc_port}",
            data=json.dumps({"method": "getmempoolinfo", "params": [], "id": 1}).encode(),
            headers={"Content-Type": "application/json"},
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            result = json.loads(resp.read().decode())
            return result.get("result")
    except Exception:
        return None


def create_mempool_embed(mempool_size, mempool_bytes, threshold):
    """Discord embed for BTC mempool congestion alert."""
    ts = local_now().strftime('%Y-%m-%d %H:%M:%S')
    mempool_mb = mempool_bytes / (1024 * 1024) if mempool_bytes else 0
    desc = (
        f"₿ Bitcoin mempool has **{mempool_size:,} unconfirmed transactions** "
        f"({mempool_mb:.1f} MB).\n\n"
        f"This indicates high network congestion. Fee rates are likely elevated.\n"
        f"Your mined blocks will earn above-average transaction fees."
    )
    fields = [
        {"name": "Transactions", "value": f"`{mempool_size:,}`", "inline": True},
        {"name": "Size", "value": f"`{mempool_mb:.1f} MB`", "inline": True},
        {"name": "Threshold", "value": f"`{threshold:,} txns`", "inline": True},
    ]
    return _embed(
        "🔴 BTC Mempool Congested",
        desc, COLORS["orange"] if "orange" in COLORS else COLORS["yellow"], fields,
        footer=f"🌀 Spiral Sentinel v{__version__} • {ts}",
    )


def check_mempool_congestion(state):
    """Alert when BTC mempool is congested beyond threshold."""
    if not CONFIG.get("mempool_alert_enabled", True):
        return
    threshold = CONFIG.get("mempool_alert_threshold", 50000)
    now = time.time()

    mempool = _get_btc_mempool_info()
    if not mempool:
        return

    mempool_size = mempool.get("size", 0)
    mempool_bytes = mempool.get("bytes", 0)

    if mempool_size <= threshold:
        return

    alert_key = "mempool_congestion"
    cooldown = ALERT_COOLDOWNS.get("mempool_congestion", 3600)
    if now - state.last_alerts.get(alert_key, 0) < cooldown:
        return

    embed = create_mempool_embed(mempool_size, mempool_bytes, threshold)
    send_alert("mempool_congestion", embed, state)
    state.last_alerts[alert_key] = now
    logger.info(f"BTC mempool congestion alert: {mempool_size:,} transactions (threshold: {threshold:,})")


# ═══════════════════════════════════════════════════════════════════════════════
# BACKUP STALENESS CHECK
# ═══════════════════════════════════════════════════════════════════════════════

def _get_newest_backup_time():
    """Return mtime of the most recently modified file/directory under /spiralpool/backups/,
    or None if the directory doesn't exist or is empty."""
    backup_root = "/spiralpool/backups"
    try:
        entries = os.listdir(backup_root)
    except OSError:
        return None
    if not entries:
        return None
    newest = max(
        os.path.getmtime(os.path.join(backup_root, e))
        for e in entries
    )
    return newest


def create_backup_stale_embed(newest_mtime, stale_days):
    """Yellow warning embed for stale (overdue) backup."""
    import datetime as _dt
    last_str = _dt.datetime.fromtimestamp(newest_mtime).strftime("%Y-%m-%d %H:%M") if newest_mtime else "never"
    age_hours = (time.time() - newest_mtime) / 3600 if newest_mtime else None
    if age_hours is None:
        age_str = "never backed up"
    elif age_hours >= 48:
        age_str = f"{age_hours / 24:.1f} days ago"
    else:
        age_str = f"{age_hours:.1f} hours ago"
    return _embed(
        "⚠️ Backup Overdue",
        f"No backup has completed in the last **{stale_days} day{'s' if stale_days != 1 else ''}**. "
        f"Last backup: `{last_str}` ({age_str}).",
        0xFFCC00,  # Yellow
        fields=[
            {"name": "📂 Backup Path", "value": "`/spiralpool/backups/`", "inline": True},
            {"name": "🔧 Run Manual Backup", "value": "`sudo spiralctl backup`", "inline": True},
        ],
    )


def check_backup_staleness(state):
    """Alert if the newest backup is older than backup_stale_days."""
    if not CONFIG.get("backup_stale_enabled", True):
        return
    # Only run if cron is installed (user opted in during install)
    if not os.path.exists("/etc/cron.d/spiralpool-backup"):
        return
    stale_days = CONFIG.get("backup_stale_days", 2)
    now = time.time()
    alert_key = "backup_stale"
    cooldown = ALERT_COOLDOWNS.get("backup_stale", 86400)
    if now - state.last_alerts.get(alert_key, 0) < cooldown:
        return

    newest_mtime = _get_newest_backup_time()
    if newest_mtime is None:
        # No backups at all — also stale
        send_alert("backup_stale", create_backup_stale_embed(None, stale_days), state)
        state.last_alerts[alert_key] = now
        logger.warning("Backup staleness alert: no backups found in /spiralpool/backups/")
        return

    age_days = (now - newest_mtime) / 86400
    if age_days >= stale_days:
        send_alert("backup_stale", create_backup_stale_embed(newest_mtime, stale_days), state)
        state.last_alerts[alert_key] = now
        logger.warning(f"Backup staleness alert: newest backup is {age_days:.1f} days old (threshold: {stale_days}d)")


# ═══════════════════════════════════════════════════════════════════════════════
# HA/VIP CONNECTION ALERTS
# ═══════════════════════════════════════════════════════════════════════════════
# Monitor HA cluster status and VIP changes

_last_ha_vip = None
_last_ha_state = None
_last_ha_role = None
_pending_role_change = None  # {"old_role": str, "new_role": str, "detected_at": float} — awaiting confirmation
HA_VIP_CHECK_INTERVAL = 30  # Check HA/VIP every 30s
# Seconds a role change must persist before it is confirmed and alerted.
# Suppresses brief keepalived VRRP election blips (MASTER→BACKUP→MASTER within N seconds).
# Real failovers hold the new role indefinitely; blips self-resolve within seconds.
# Default 90s: covers typical VRRP election re-convergence while still catching real outages promptly.
HA_ROLE_CHANGE_CONFIRM_SECS = CONFIG.get("ha_role_change_confirm_secs", 90)
_last_ha_vip_check = 0


def check_ha_vip_changes():
    """Check for HA/VIP state changes.

    Returns:
        dict: Change info or None if no changes
    """
    global _last_ha_vip, _last_ha_state, _last_ha_role, _last_ha_vip_check, _pending_role_change

    current_time = time.time()
    if current_time - _last_ha_vip_check < HA_VIP_CHECK_INTERVAL:
        return None
    _last_ha_vip_check = current_time

    if not _HA_AVAILABLE or not _ha_manager:
        return None

    try:
        status = _ha_manager.get_status()
        if not status or not status.enabled:
            return None

        current_vip = status.vip
        current_state = status.state
        current_role = status.local_role

        changes = []

        # Check for VIP change
        if _last_ha_vip is not None and current_vip != _last_ha_vip:
            changes.append({
                "type": "vip_change",
                "old_vip": _last_ha_vip,
                "new_vip": current_vip,
                "role": current_role,
            })

        # Check for state change (running -> failover, etc.)
        if _last_ha_state is not None and current_state != _last_ha_state:
            changes.append({
                "type": "ha_state_change",
                "old_state": _last_ha_state,
                "new_state": current_state,
                "role": current_role,
            })

        # Check for role change — debounced to suppress brief VRRP blips.
        # keepalived elections can cause MASTER→BACKUP→MASTER within seconds;
        # alerting on these wakes operators for nothing.
        #
        # A role change is only confirmed (and alerted) once it has held for
        # HA_ROLE_CHANGE_CONFIRM_SECS (default 90s). If the node reverts to its
        # original role within that window, the blip is silently suppressed.
        if _pending_role_change is not None:
            elapsed = current_time - _pending_role_change["detected_at"]
            if current_role == _pending_role_change["old_role"]:
                # Reverted to original role — blip regardless of how long it took
                logger.info(
                    f"HA role blip suppressed: {_pending_role_change['old_role']} → "
                    f"{_pending_role_change['new_role']} → {current_role} "
                    f"(self-resolved in {elapsed:.0f}s)"
                )
                _pending_role_change = None
            elif current_role != _pending_role_change["new_role"]:
                # Changed to a third role while still pending — update target, reset timer
                logger.info(
                    f"HA role changed again while pending: "
                    f"{_pending_role_change['new_role']} → {current_role} (resetting timer)"
                )
                _pending_role_change["new_role"] = current_role
                _pending_role_change["detected_at"] = current_time
            elif elapsed >= HA_ROLE_CHANGE_CONFIRM_SECS:
                # Still at new_role and held long enough — confirmed real change
                changes.append({
                    "type": "role_change",
                    "old_role": _pending_role_change["old_role"],
                    "new_role": current_role,
                })
                logger.info(
                    f"HA role change confirmed after {elapsed:.0f}s: "
                    f"{_pending_role_change['old_role']} → {current_role}"
                )
                _pending_role_change = None
            else:
                # Still pending, debounce window not yet elapsed
                logger.debug(
                    f"HA role change pending: {_pending_role_change['old_role']} → {current_role} "
                    f"({elapsed:.0f}s / {HA_ROLE_CHANGE_CONFIRM_SECS}s)"
                )
        elif _last_ha_role is not None and current_role != _last_ha_role:
            # New role change detected — stage for timed confirmation, do not alert yet
            logger.info(
                f"HA role change detected (confirming in {HA_ROLE_CHANGE_CONFIRM_SECS}s): "
                f"{_last_ha_role} → {current_role}"
            )
            _pending_role_change = {
                "old_role": _last_ha_role,
                "new_role": current_role,
                "detected_at": current_time,
            }

        # Update tracked VIP and state immediately; role only advances when not
        # in pending confirmation (keeps _last_ha_role at pre-change value so the
        # next cycle can correctly detect a revert).
        old_vip = _last_ha_vip
        old_state = _last_ha_state
        old_role = _last_ha_role
        _last_ha_vip = current_vip
        _last_ha_state = current_state
        if _pending_role_change is None:
            _last_ha_role = current_role

        # Resolve local node's host IP from the cluster node list
        local_host = "unknown"
        for node in status.nodes:
            if node.id == status.local_id:
                local_host = node.host
                break

        if changes:
            return {
                "changes": changes,
                "old_vip": old_vip,
                "new_vip": current_vip,
                "old_state": old_state,
                "new_state": current_state,
                "old_role": old_role,
                "role": current_role,
                "vip": current_vip,
                "local_host": local_host,
                "node_count": len(status.nodes),
            }

        return None
    except Exception as e:
        logger.debug(f"Error checking HA/VIP changes: {e}")
        return None


def create_vip_change_embed(old_vip, new_vip, role, node_count):
    """Create Discord embed for VIP change alert."""
    role_emoji = {"MASTER": "👑", "BACKUP": "🔄", "OBSERVER": "👁️"}.get(role, "❓")

    desc = f"""```fix
{theme("ha.vip_change.banner")}
```

{theme("ha.vip_change.body")}

**Old VIP:** `{old_vip or 'None'}`
**New VIP:** `{new_vip or 'None'}`

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {"name": "This Node", "value": f"{role_emoji} {role}", "inline": True},
        {"name": "Cluster Size", "value": f"`{node_count}` nodes", "inline": True},
    ]

    return _embed(theme("ha.vip_change.title"), desc, COLORS["cyan"], fields,
                  footer=f"🌀 Spiral Sentinel v{__version__} • {theme('ha.vip_change.footer')}")


def create_ha_state_change_embed(old_state, new_state, role):
    """Create Discord embed for HA state change alert."""
    role_emoji = {"MASTER": "👑", "BACKUP": "🔄", "OBSERVER": "👁️"}.get(role, "❓")

    # Determine severity based on state transition
    if new_state == "failover":
        title = theme("ha.failover.title")
        color = COLORS["orange"]
        banner = theme("ha.failover.banner")
        banner_style = "diff\n-"
        body = theme("ha.failover.body")
        footer_text = theme("ha.failover.footer")
    elif new_state == "degraded":
        title = theme("ha.degraded.title")
        color = COLORS["red"]
        banner = theme("ha.degraded.banner")
        banner_style = "diff\n-"
        body = theme("ha.degraded.body")
        footer_text = theme("ha.degraded.footer")
    elif new_state == "running" and old_state in ["failover", "degraded"]:
        title = theme("ha.recovered.title")
        color = COLORS["green"]
        banner = theme("ha.recovered.banner")
        banner_style = "diff\n+"
        body = theme("ha.recovered.body")
        footer_text = theme("ha.recovered.footer")
    else:
        title = theme("ha.state_change.title", state=new_state.upper())
        color = COLORS["blue"]
        banner = f"STATE: {old_state.upper()} → {new_state.upper()}"
        banner_style = "fix\n"
        body = theme("ha.state_change.body", old=old_state, new=new_state)
        footer_text = f"State: {old_state} → {new_state}"

    desc = f"""```{banner_style} {banner}
```

{body}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {"name": "Previous State", "value": f"`{(old_state or 'unknown').upper()}`", "inline": True},
        {"name": "Current State", "value": f"`{new_state.upper()}`", "inline": True},
        {"name": "This Node", "value": f"{role_emoji} {role}", "inline": True},
    ]

    return _embed(title, desc, color, fields,
                  footer=f"🌀 Spiral Sentinel v{__version__} • {footer_text}")


def handle_ha_vip_alerts(change_info, state):
    """Process HA/VIP changes and send alerts."""
    if not change_info or not change_info.get("changes"):
        return

    for change in change_info["changes"]:
        change_type = change["type"]

        if change_type == "vip_change":
            embed = create_vip_change_embed(
                change_info["old_vip"],
                change_info["new_vip"],
                change_info["role"],
                change_info.get("node_count", 0)
            )
            send_alert("ha_vip_change", embed, state)
            logger.info(f"HA VIP changed: {change_info['old_vip']} → {change_info['new_vip']}")

        elif change_type == "ha_state_change":
            embed = create_ha_state_change_embed(
                change_info["old_state"],
                change_info["new_state"],
                change_info["role"]
            )
            send_alert("ha_state_change", embed, state)
            logger.info(f"HA state changed: {change_info['old_state']} → {change_info['new_state']}")

        elif change_type == "role_change":
            old_role = change.get("old_role", "UNKNOWN")
            new_role = change.get("new_role", "UNKNOWN")
            node_ip = change_info.get("local_host", "unknown")
            vip = change_info.get("vip", "unknown")

            if new_role == "MASTER":
                # Node promoted to MASTER — services starting
                embed = create_ha_promoted_embed(
                    node_ip, new_role, vip,
                    reason=f"Role changed from {old_role}"
                )
                send_alert("ha_promoted", embed, state)
                logger.info(f"HA node promoted: {old_role} → {new_role} (node: {node_ip})")

            elif old_role == "MASTER":
                # Node demoted from MASTER — services stopping
                embed = create_ha_demoted_embed(
                    node_ip, old_role, new_role,
                    reason=f"Role changed to {new_role}"
                )
                send_alert("ha_demoted", embed, state)
                logger.info(f"HA node demoted: {old_role} → {new_role} (node: {node_ip})")

            else:
                # Non-master role change (e.g., OBSERVER → BACKUP)
                logger.info(f"HA role changed: {old_role} → {new_role} (node: {node_ip})")


# Report currency configuration — any of 10 supported currencies
# Default to CAD to match installer defaults
REPORT_CURRENCY = CONFIG.get("report_currency", "CAD")
POWER_CURRENCY = CONFIG.get("power_currency", "CAD")
POWER_RATE_KWH = CONFIG.get("power_rate_kwh", 0.12)

# ═══════════════════════════════════════════════════════════════════════════════
# SUPPORTED CURRENCIES — centralized metadata for all 10 fiat currencies
# Used by CoinGecko API calls, report formatting, and price display
# ═══════════════════════════════════════════════════════════════════════════════
SUPPORTED_CURRENCIES = {
    "USD": {"symbol": "$", "emoji": "🦅", "name": "US Dollar", "code": "usd", "decimals": 2},
    "CAD": {"symbol": "$", "emoji": "🍁", "name": "Canadian Dollar", "code": "cad", "decimals": 2},
    "EUR": {"symbol": "€", "emoji": "🌐", "name": "Euro", "code": "eur", "decimals": 2},
    "GBP": {"symbol": "£", "emoji": "🌐", "name": "British Pound", "code": "gbp", "decimals": 2},
    "JPY": {"symbol": "¥", "emoji": "🌐", "name": "Japanese Yen", "code": "jpy", "decimals": 0},
    "AUD": {"symbol": "$", "emoji": "🌐", "name": "Australian Dollar", "code": "aud", "decimals": 2},
    "CHF": {"symbol": "Fr.", "emoji": "🌐", "name": "Swiss Franc", "code": "chf", "decimals": 2},
    "CNY": {"symbol": "¥", "emoji": "🌐", "name": "Chinese Yuan", "code": "cny", "decimals": 2},
    "NZD": {"symbol": "$", "emoji": "🌐", "name": "New Zealand Dollar", "code": "nzd", "decimals": 2},
    "SEK": {"symbol": "kr", "emoji": "🌐", "name": "Swedish Krona", "code": "sek", "decimals": 2},
}

# CoinGecko vs_currencies parameter — fetch all supported currencies in one call
VS_CURRENCIES = ",".join(c["code"] for c in SUPPORTED_CURRENCIES.values())

# Lowercase currency codes for dict lookups
CURRENCY_CODES = VS_CURRENCIES.split(",")

# All supported coin symbols (lowercase, for price dict keys)
SUPPORTED_COIN_SYMBOLS = ["dgb", "btc", "bch", "bc2", "qbx", "nmc", "sys", "xmy", "fbtc", "ltc", "doge", "dgb-scrypt", "pep", "cat"]

# Default block rewards by coin (used as fallback when API data unavailable)
DEFAULT_BLOCK_REWARDS = {
    # SHA-256d coins
    "DGB": 277.38, "BTC": 3.125, "BCH": 3.125, "BC2": 50.0, "QBX": 12.5,
    # SHA-256d merge-mineable aux chains
    "NMC": 6.25, "SYS": 1.25, "XMY": 500.0, "FBTC": 25.0,
    # Scrypt coins
    "LTC": 6.25, "DOGE": 10000, "DGB-SCRYPT": 277.38,
    "PEP": 50, "CAT": 25,
}


# Block explorer URL templates by coin.
# {hash} is substituted with the block hash when available.
# These are the canonical public explorers for each supported coin.
BLOCK_EXPLORER_URLS = {
    "BTC":       "https://mempool.space/block/{hash}",
    "BCH":       "https://blockchair.com/bitcoin-cash/block/{hash}",
    "BC2":       None,
    "DGB":       "https://digiexplorer.info/block/{hash}",
    "QBX":       None,
    "NMC":       "https://bchain.info/NMC/block/{hash}",
    "SYS":       "https://blockchair.com/syscoin/block/{hash}",
    "XMY":       None,
    "FBTC":      "https://explorer.fractalbitcoin.io/block/{hash}",
    "LTC":       "https://blockchair.com/litecoin/block/{hash}",
    "DOGE":      "https://blockchair.com/dogecoin/block/{hash}",
    "DGB-SCRYPT": "https://digiexplorer.info/block/{hash}",
    "PEP":       None,
    "CAT":       None,
}


def get_block_explorer_url(coin_symbol, block_hash):
    """Return a block explorer URL for the given coin and block hash, or None."""
    if not block_hash or not coin_symbol:
        return None
    template = BLOCK_EXPLORER_URLS.get(coin_symbol.upper())
    if not template:
        return None
    return template.format(hash=block_hash)


def get_currency_meta(currency_code=None):
    """Get metadata dict for a currency. Defaults to REPORT_CURRENCY."""
    code = currency_code or REPORT_CURRENCY
    return SUPPORTED_CURRENCIES.get(code, SUPPORTED_CURRENCIES["USD"])


def format_wallet_fiat(amount, prices):
    """Format coin_amount * price in user's preferred currency.

    prices dict must have lowercase currency code keys (e.g., 'usd', 'eur').
    """
    cur = get_currency_meta()
    fiat_val = amount * prices.get(cur["code"], 0)
    return f"{cur['emoji']} {cur['symbol']}{fiat_val:,.{cur['decimals']}f}"


def format_coin_price_yaml(coin_symbol, prices):
    """Format coin exchange rate in yaml code block for reports.

    Example: ```yaml\nDGB/USD: $0.0123\n```
    """
    cur = get_currency_meta()
    price = prices.get(cur["code"], 0)
    return f"```yaml\n{coin_symbol}/{REPORT_CURRENCY}: {cur['symbol']}{price:.4f}\n```"


def format_value_block(value_dict):
    """Format earnings total in diff code block.

    value_dict has lowercase currency code keys (e.g., 'usd', 'cad', 'eur').
    """
    cur = get_currency_meta()
    val = value_dict.get(cur["code"], 0)
    return f"```diff\n+ {cur['symbol']}{val:,.{cur['decimals']}f} {REPORT_CURRENCY}\n```"


def format_fiat_inline(amount, prices, prefix=""):
    """Format coin_amount * price as inline text with emoji.

    For revenue lost, payout values, etc. Returns string with leading newline.
    """
    lines = ""
    cur = get_currency_meta()
    fiat_val = amount * prices.get(cur["code"], 0)
    if fiat_val > 0:
        lines += f"\n{cur['emoji']} `{cur['symbol']}{fiat_val:,.{cur['decimals']}f} {REPORT_CURRENCY}`"
    return lines


def compute_portfolio_total(coin_amounts, all_prices, currency_code=None):
    """Sum coin_amount * price for all coins in a given fiat currency.

    coin_amounts: dict with coin symbol keys (e.g., 'total_dgb', 'total_btc')
    all_prices: flat dict from fetch_all_prices() with '{coin}_{currency}' keys
    currency_code: lowercase code (e.g., 'usd'). Defaults to preferred currency.
    """
    if currency_code is None:
        currency_code = get_currency_meta()["code"]
    total = 0
    for coin in SUPPORTED_COIN_SYMBOLS:
        total += coin_amounts.get(f"total_{coin}", 0) * all_prices.get(f"{coin}_{currency_code}", 0)
    return total


# ═══════════════════════════════════════════════════════════════════════════════
# ALERT THEME SYSTEM — cyberpunk (default) or professional
# Only flavor text, emojis, and banner art change. Detection logic, data fields,
# colors, and structure stay identical.
# ═══════════════════════════════════════════════════════════════════════════════
ALERT_THEME = CONFIG.get("alert_theme", "cyberpunk").lower()

ALERT_THEMES = {
    "cyberpunk": {
        # Startup
        "startup.banner": "SENTINEL ACTIVATED",
        "startup.status": "All systems nominal",
        "startup.footer": "Monitoring {total} miners \u2022 {coin_name} Solo Pool",
        # Block found
        "block.title": "\U0001f38a {coin} BLOCK CAPTURED! \U0001f38a",
        "block.banner": "\U0001f389 BLOCK CAPTURED! \U0001f389",
        "block.hero": "\U0001f525 **You flatlined the network, choom!** \U0001f525",
        "block.sub": "\U0001f480 *Another one bites the dust...*",
        "block.found_by": "struck gold!",
        "block.footer": "SOLO VICTORY!",
        # Block orphaned
        "orphaned.title": "\u26a0\ufe0f {coin} BLOCK ORPHANED \u26a0\ufe0f",
        "orphaned.banner_emoji": "\U0001f480",
        "orphaned.flavor": "A previously found block has been orphaned!",
        # Miner offline
        "offline.title": "\U0001f6a8 RIG FLATLINED",
        "offline.banner": "MINER WENT OFFLINE",
        "offline.footer": "Sentinel watching for recovery",
        "offline.footer_noip": "Sentinel watching for recovery \u2022 Check network/power",
        # Miner online
        "online.title": "\u2705 RIG BACK ONLINE",
        "online.banner": "RIG BACK ON THE GRID",
        "online.flavor": "has recovered and is hashing!",
        "online.footer": "Sentinel confirms connection restored",
        # Restart
        "restart.title": "\U0001f504 AUTO-RESTART INITIATED",
        "restart.success_banner": "Recovery protocol initiated",
        "restart.fail_banner": "Restart attempt failed",
        "restart.footer": "Sentinel auto-recovery system",
        # Zombie
        "zombie.title": "\U0001f9df ZOMBIE MINER",
        "zombie.banner": "\U0001f9df ZOMBIE DETECTED \U0001f9df",
        "zombie.flavor": "is consuming power without earning rewards",
        "zombie.footer": "Auto-restart triggered \u2022 Stay frosty, choom",
        # Degradation
        "degradation.title": "\U0001f4c9 HASHRATE DEGRADATION",
        "degradation.footer": "Sentinel monitoring performance",
        # Orphan spike
        "orphan_spike.banner_emoji": "\U0001f480",
        "orphan_spike.title": "\U0001f480 ORPHAN RATE SPIKE",
        # Opportunity - crash
        "opportunity.crash.title": "NETWORK HASHRATE DROP",
        "opportunity.crash.banner": "\U0001f3af HUNTING WINDOW OPEN \U0001f3af",
        "opportunity.crash.flavor": "**{severity} CRASH!** Network hashrate dropped - strike now!",
        "opportunity.crash.footer": "\U0001f300 {coin} \u2022 Network dropped {drop_pct:.0f}% \u2022 Your odds improved!",
        # Opportunity - high odds
        "opportunity.high_odds.title": "HIGH ODDS ALERT",
        "opportunity.high_odds.banner": "\U0001f3af PRIME CONDITIONS! \U0001f3af",
        "opportunity.high_odds.flavor": "\U0001f525 **{odds_status} odds detected!** Strike while the iron is hot!",
        "opportunity.high_odds.footer": "\U0001f300 {coin} \u2022 {time} \u2022 Go get 'em, choom!",
        # Pool hashrate drop
        "pool_drop.title": "\U0001f4c9 FLEET HASHRATE DROP",
        "pool_drop.banner": "FLEET HASHRATE DROP DETECTED",
        "pool_drop.body": "\u26a0\ufe0f **{severity}** - Pool hashrate dropped **{drop_pct:.1f}%**",
        "pool_drop.footer": "\U0001f527 Check your rigs, choom!",
        # Mode switch
        "mode_switch.solo_to_multi": "\U0001f504 MODE SWITCH: SOLO → MULTI-COIN",
        "mode_switch.multi_to_solo": "\U0001f504 MODE SWITCH: MULTI → SOLO ({coin})",
        "mode_switch.footer": "Mode changed at {time}",
        # Consolidated coin config
        "coin_config.mode_switch": "\U0001f504 POOL CONFIG: {old} → {new}",
        "coin_config.coins_changed": "\U0001f504 POOL CONFIG: COINS CHANGED",
        "coin_config.coins_added": "➕ POOL CONFIG: +{count} COIN{plural}",
        "coin_config.coins_removed": "➖ POOL CONFIG: -{count} COIN{plural}",
        "coin_config.footer": "Config changed at {time}",
        # Update notification
        "update.title": "\U0001f504 SPIRAL POOL UPDATE AVAILABLE",
        "update.body": "A new version is available! Time to upgrade, choom.",
        "update.footer": "Sentinel Update Check \u2022 Run upgrade when ready",
        # Best share
        "best_share.title": "\U0001f3c6 NEW BEST SHARE!",
        "best_share.flavor": "Your fleet submitted a new **all-time best share**!",
        "best_share.footer": "Personal best \u2022 The closest your fleet has come to a block",
        # Miner reboot
        "reboot.title": "\u26a1 RIG REBOOTED",
        "reboot.flavor": "appears to have rebooted",
        "reboot.footer": "Sentinel monitoring \u2022 Miner recovered",
        # Power event
        "power_event.title": "\u26a1 GRID POWER EVENT",
        "power_event.footer": "Check your power infrastructure, choom!",
        # Orphan spike
        "orphan_spike.footer": "Block revenue lost \u2022 Check node propagation",
        # Revenue decline
        "revenue_decline.title": "\U0001f4c9 REVENUE PACE DECLINING",
        # HA - VIP Change
        "ha.vip_change.title": "🔄 VIP REASSIGNED",
        "ha.vip_change.banner": "VIP REASSIGNED",
        "ha.vip_change.body": "The cluster Virtual IP has been reassigned.",
        "ha.vip_change.footer": "Cluster VIP changed • Miners auto-reconnecting",
        # HA - Failover
        "ha.failover.title": "⚠️ HA FAILOVER IN PROGRESS",
        "ha.failover.banner": "FAILOVER IN PROGRESS",
        "ha.failover.body": "A node went dark. The grid is reconfiguring, choom.",
        "ha.failover.footer": "Failover active • Services transitioning",
        # HA - Degraded
        "ha.degraded.title": "🔴 CLUSTER DEGRADED",
        "ha.degraded.banner": "CLUSTER DEGRADED",
        "ha.degraded.body": "One or more nodes flatlined. Redundancy compromised.",
        "ha.degraded.footer": "Cluster degraded • Check node status",
        # HA - Recovered
        "ha.recovered.title": "✅ CLUSTER RECOVERED",
        "ha.recovered.banner": "CLUSTER RECOVERED",
        "ha.recovered.body": "The grid is back to full strength.",
        "ha.recovered.footer": "Cluster recovered • All nodes operational",
        # HA - State Change (generic)
        "ha.state_change.title": "🔄 HA STATE: {state}",
        "ha.state_change.body": "HA cluster state changed from `{old}` to `{new}`.",
        # HA - Replica Drop
        "ha.replica_drop.title": "🔗 REPLICA FLATLINED",
        "ha.replica_drop.banner": "REPLICA FLATLINED",
        "ha.replica_drop.body": "HA replica count dropped — cluster redundancy compromised.",
        "ha.replica_drop.footer": "Cluster redundancy lost • Check replica nodes, choom",
        # HA - Promoted
        "ha.promoted.title": "👑 NODE PROMOTED TO MASTER",
        "ha.promoted.banner": "NODE PROMOTED",
        "ha.promoted.body": "This node has taken the crown. All services starting.",
        "ha.promoted.footer": "Node promoted • Services coming online",
        # HA - Demoted
        "ha.demoted.title": "🔄 NODE DEMOTED TO BACKUP",
        "ha.demoted.banner": "NODE DEMOTED",
        "ha.demoted.body": "This node is standing down. Services stopping.",
        "ha.demoted.footer": "Node demoted • Services stopped on this node",
        # HA - Replication Lag
        "ha.replication_lag.title": "⏱️ REPLICATION LAG WARNING",
        "ha.replication_lag.banner": "REPLICATION LAG",
        "ha.replication_lag.body": "Database replication is falling behind. Failover readiness reduced.",
        "ha.replication_lag.footer": "Replication lag • Check network and disk I/O",
        # HA - Resync Estimate
        "ha.resync.title": "🔄 POST-FAILOVER RESYNC",
        "ha.resync.banner": "RESYNC IN PROGRESS",
        "ha.resync.body": "Backup node is catching up after outage.",
        "ha.resync.footer": "Resync active • ETA based on current throughput",
        # HA - Mode Toggle
        "ha.enabled.title": "✅ HA MODE ACTIVATED",
        "ha.enabled.body": "High availability engaged. The grid is now redundant, choom.",
        "ha.enabled.footer": "HA cluster active • Failover protection online",
        "ha.disabled.title": "🔴 HA MODE DISABLED",
        "ha.disabled.body": "High availability is offline. Running solo — no safety net.",
        "ha.disabled.footer": "HA disabled • Single-node operation",
        # Infrastructure
        "infra.circuit_breaker.title": "🔴 CIRCUIT BREAKER OPEN",
        "infra.circuit_breaker.banner": "CIRCUIT BREAKER OPEN",
        "infra.circuit_breaker.body": "Shares are being REJECTED — miners hashing but work not counted",
        "infra.circuit_breaker.footer": "CRITICAL: Pool rejecting shares • Check pool infrastructure, choom",
        "infra.backpressure.title": "BACKPRESSURE {level}",
        "infra.backpressure.banner": "BACKPRESSURE {level}",
        "infra.backpressure.body": "Pool buffer under pressure — shares may be dropped",
        "infra.backpressure.footer": "Pool buffer under pressure • Check pool I/O capacity",
        "infra.wal_errors.title": "💾 DATABASE WRITE ERRORS",
        "infra.wal_errors.banner": "DATABASE WRITE ERRORS",
        "infra.wal_errors.body": "Accepted shares may NOT be persisting to disk — silent data loss",
        "infra.wal_errors.footer": "CRITICAL: Share data may be lost • Check disk and database, choom",
        "infra.zmq_disconnected.title": "📡 ZMQ CONNECTION LOST",
        "infra.zmq_disconnected.banner": "ZMQ CONNECTION LOST",
        "infra.zmq_disconnected.body": "Block notifications are NOT being received in real-time",
        "infra.zmq_disconnected.footer": "Real-time block detection impaired • Check ZMQ endpoint",
        "infra.zmq_stale.title": "📡 ZMQ STALE",
        "infra.zmq_stale.banner": "ZMQ MESSAGE STALE",
        "infra.zmq_stale.body": "Block detection may be delayed — falling back to RPC polling",
        "infra.zmq_stale.footer": "Block notification delay • Check ZMQ connectivity",
        "infra.alert.title": "{prefix} {icon} Infrastructure Alert",
        "infra.alert.footer": "Alert type: {alert_type} | Severity: {severity}",
        "infra.api.title": "{prefix} {title}{coin}",
        "infra.api.footer": "Pool API Sentinel • {alert_type}",
        # Reports & Status
        # HA Status
        "ha.status.title": "🔄 HA CLUSTER STATUS",
        "ha.status.body": "Node `{node}` is running **{role}** on the grid",
        "ha.status.footer": "VIP: {vip} • Grid uplink active",
        # Infrastructure Health
        "infra.health.title": "🏗️ GRID DIAGNOSTICS — {status}",
        "infra.health.body": "Backend metrics and netrunner diagnostics",
        "infra.health.footer": "🌀 Spiral Sentinel • Grid Diagnostics",
        # 6-Hour / Daily Intel Report
        "report.title_daily": "{coin_emoji} DAILY INTEL BRIEF",
        "report.title_goodnight": "🛌😴💤 LIGHTS OUT — 6-HOUR INTEL REPORT",
        "report.title_morning": "🌅🌞☕ RISE AND GRIND — 6-HOUR INTEL REPORT",
        "report.title_default": "{coin_emoji} 6-HOUR INTEL REPORT",
        "report.footer": "🌀 Spiral Sentinel v{version} • Next intel drop in {next_report}",
        # Weekly Summary
        "weekly.title": "📈 WEEKLY DEBRIEF",
        "weekly.banner": "📈 WEEKLY DEBRIEF 📈",
        "weekly.footer": "🌀 Spiral Sentinel v{version}",
        # Monthly Earnings
        "monthly.title": "📅 MONTHLY HAUL",
        "monthly.banner": "💰 MONTHLY HAUL 💰",
        "monthly.footer": "🌀 Spiral Sentinel v{version} • Don't forget monthly maintenance, choom",
        # Maintenance Reminder
        "maintenance.title": "🔧 MONTHLY RIG CHECK",
        "maintenance.banner": "🔧 MONTHLY RIG CHECK",
        "maintenance.intro": "Time to tune up the rigs, choom!",
        "maintenance.footer": "🌀 Spiral Sentinel v{version} • Well-tuned rigs = maximum hash",
        # Quarterly Report
        "quarterly.title": "📅 {quarter} QUARTERLY DEBRIEF",
        "quarterly.body": "*Quarter ending {date}*",
        # Special Date
        "special.title": "{emoji} {name} MINING REPORT",
        "special.spring_equinox": "🌸 New season, same grind. The chain doesn't care about daylight — neither do we, choom.",
        "special.summer_solstice": "☀️ Peak sunlight, peak solar hash potential. Longest day of the year — make every watt count.",
        "special.autumn_equinox": "🍂 Summer's over but the rigs don't hibernate. Cooler air means better thermals — overclock season.",
        "special.winter_solstice": "❄️ Shortest day, longest night. Your rigs are space heaters now — free warmth and free sats.",
        "special.new_years": "🎆 New year, new blocks. The chain rolled over midnight without missing a beat — and so did your rigs.",
        "special.christmas": "🎄 Best gift? A solved block under the tree. Keep the rigs humming while everyone else sleeps.",
        "special.easter": "🐣 New blocks hatching. The mempool doesn't take holidays — stay stacking, choom.",
        "special.good_friday": "✝️ A day of rest for some. The blockchain rests for no one — keep those shares flowing.",
        "special.canada_day": "🍁 Happy Canada Day! Cheap hydro power + cold winters = built for mining. True North strong and hashing.",
        "special.victoria_day": "👑 Long weekend, long hashes. Enjoy the fireworks — your rigs are already making sparks.",
        "special.civic_holiday": "🏖️ August long weekend. Perfect time to check those thermal pads while the pool runs itself.",
        "special.labour_day": "⚒️ Your rigs don't get the day off. That's the beauty of proof-of-work — it works while you rest.",
        "special.thanksgiving_ca": "🦃 Grateful for low difficulty and high uptime. May your turkey be as golden as your block rewards.",
        "special.remembrance_day": "🌺 Lest we forget. Mining runs 24/7/365 — even on days that matter most.",
        "special.boxing_day": "🎁 Best deal of the year? A solo block. No coupon needed — just hashpower and patience.",
        "special.family_day": "👨‍👩‍👧‍👦 Spend the day with family. Your rigs will hold the line — that's what automation is for.",
        "special.indigenous_day": "🪶 Honouring Indigenous peoples and their enduring connection to this land. The network runs on shared ground.",
        "special.truth_reconciliation": "🧡 A day to remember and reflect. The work of reconciliation continues — one step at a time.",
        "special.independence_day": "🦅 Freedom to mine, freedom to self-custody. Decentralization is independence — happy 4th, choom.",
        "special.mlk_day": "✊ Proof-of-work doesn't discriminate. Every valid hash counts equally. Keep building.",
        "special.presidents_day": "🏛️ No executive order can stop the blockchain. Permissionless by design — keep hashing.",
        "special.memorial_day": "🎖️ Honoring those who served. Your rigs serve the network — block by block, share by share.",
        "special.labor_day": "⚒️ Your rigs don't get the day off. That's the beauty of proof-of-work — it works while you rest.",
        "special.columbus_day": "🚢 Exploring new blocks, one hash at a time. Fortune favors the persistent miner.",
        "special.thanksgiving_us": "🦃 Grateful for low difficulty and high uptime. May your block rewards be as generous as the feast.",
        "special.juneteenth": "✊🏿 Freedom isn't free — and neither is decentralization. Celebrating liberation and self-sovereignty.",
        "special.veterans_day": "🎖️ Saluting those who served. The network stands strong because miners stand watch — 24/7.",
        "special.fallback": "🌟 Special day, same mission. The blockchain never stops — and neither does your pool.",
        # Consolidated Report
        "consolidated.title_suffix": "INTEL REPORT",
        # Trend Analysis
        "trend.title": "📊 {coin} TREND ANALYSIS",
        # Difficulty Report
        "difficulty.title": "🎯 {coin} DIFFICULTY REPORT{algo}",
        # Achievement
        "achievement.title": "🏆 ACHIEVEMENT UNLOCKED!",
        "achievement.footer": "🌀 Spiral Sentinel v{version} • Achievement System",
        # Miner/Hardware
        "temp.warning_title": "🌡️ THERMAL WARNING",
        "temp.critical_title": "🔥 THERMAL CRITICAL",
        "temp.warning_banner": "Elevated temperature detected",
        "temp.critical_banner": "DANGER: OVERHEATING DETECTED!",
        "temp.warning_footer": "Monitor closely • Check ambient temp and airflow",
        "temp.critical_footer": "⚠️ IMMEDIATE ACTION REQUIRED - Check fans and airflow!",
        "thermal.title": "THERMAL SHUTDOWN",
        "thermal.banner": "THERMAL EMERGENCY",
        "thermal.stopped": "YOUR MINER HAS BEEN STOPPED",
        "thermal.failed": "COULD NOT STOP MINER - UNPLUG IMMEDIATELY",
        "thermal.footer": "CRITICAL: Manual intervention required to restore mining",
        "fan.title": "🌀 FAN FAILURE",
        "fan.banner": "FAN FAILURE DETECTED",
        "fan.body": "Check cooling system immediately",
        "fan.footer": "Fan at 0 RPM while miner is running • Check cooling immediately",
        "hashboard.title": "🪫 HASHBOARD DEAD",
        "hashboard.banner": "HASHBOARD FAILURE",
        "hashboard.footer": "Discrete hardware failure • Board replacement may be needed",
        "hw_error.title": "⚠️ HARDWARE ERROR RATE",
        "hw_error.banner": "HARDWARE ERROR RATE",
        "hw_error.body": "Rising hardware errors predict ASIC chip failure",
        "hw_error.footer": "Predictive: rising HW errors indicate impending chip failure",
        "rejection.title": "📊 SHARE REJECTION SPIKE",
        "rejection.banner": "SHARE REJECTION SPIKE",
        "rejection.body": "Shares are being rejected at an abnormal rate",
        "rejection.footer": "High rejection rate wastes hashpower • Check miner config",
        "url_mismatch.title": "🔒 STRATUM URL MISMATCH",
        "url_mismatch.banner": "STRATUM URL MISMATCH",
        "url_mismatch.body": "This could indicate firmware hijacking, misconfiguration, or failover",
        "url_mismatch.footer": "SECURITY: Verify miner firmware and pool configuration",
        "chronic.title": "🔁 CHRONIC ISSUE DETECTED",
        "chronic.banner": "🔁 CHRONIC ISSUE 🔁",
        "chronic.body": "has a recurring problem that needs attention!",
        "chronic.footer": "Manual inspection recommended",
        "excessive_restarts.title": "⚡ EXCESSIVE RESTARTS",
        "excessive_restarts.banner": "EXCESSIVE RESTARTS",
        "excessive_restarts.body": "Frequent reboots indicate hardware or power issues.",
        "excessive_restarts.footer": "Sentinel detected restart loop • Check power and cooling",
        # Performance
        "divergence.title": "📡 HASHRATE DIVERGENCE",
        "divergence.banner": "HASHRATE DIVERGENCE DETECTED",
        "divergence.footer": "Pool not receiving expected hashrate • Check network and shares",
        "worker_drop.title": "👷 WORKER COUNT DROP",
        "worker_drop.banner": "WORKER COUNT DROP",
        "worker_drop.body": "Multiple workers may have disconnected",
        "worker_drop.footer": "Workers disconnected • Check fleet connectivity",
        "share_loss.title": "📉 SHARE LOSS RATE",
        "share_loss.banner": "SHARE LOSS RATE ELEVATED",
        "share_loss.body": "Shares are being lost between miner and pool accounting",
        "share_loss.footer": "Shares being dropped • Check pool infrastructure",
        "notify_mode.title": "📡 BLOCK NOTIFY MODE",
        "notify_mode.banner": "BLOCK NOTIFY MODE CHANGE",
        "notify_mode.footer": "Block detection method changed",
        # Financial
        "sats_surge.title": "📈 {coin_emoji} {coin_name} SAT SURGE ALERT",
        "sats_surge.body": "sat value has increased significantly against BTC.",
        "sats_surge.footer": "Sats Surge Tracker",
        "price_crash.title": "📉 {coin} PRICE CRASH",
        "price_crash.body": "has dropped significantly in the last hour.",
        "price_crash.footer": "Price crash detection • {threshold}%+ drop threshold",
        "payout.title": "💰 {coin} PAYOUT RECEIVED",
        "payout.body": "payout detected!",
        "payout.footer": "Payout confirmed on-chain",
        "wallet_drop.title": "🚨 {coin} WALLET BALANCE DROP",
        "wallet_drop.body": "wallet balance dropped unexpectedly.",
        "wallet_drop.footer": "Verify wallet transaction history",
        "missing_payout.title": "⚠️ {coin} MISSING PAYOUT",
        "missing_payout.body": "wallet balance has not changed in {days} days.",
        "missing_payout.footer": "No balance change in {days}d",
        # Coin config
        "coin.node_down.title": "🔴 NODE DOWN: {coin_emoji} {coin}",
        "coin.node_down.banner": "{coin_name} NODE UNREACHABLE",
        "coin.node_down.body": "node is offline! The pool cannot communicate with this blockchain node.",
        "coin.node_down.footer": "Node down at {time}",
        "coin.sync_behind.title": "⏳ SYNC BEHIND: {coin_emoji} {coin}",
        "coin.sync_behind.banner": "{coin_name} node syncing",
        "coin.sync_behind.body": "Mining efficiency may be affected until the node catches up.",
        "coin.sync_behind.footer": "Sync issue at {time}",
        "coin.recovered.title": "✅ NODE RECOVERED: {coin_emoji} {coin}",
        "coin.recovered.banner": "{coin_name} NODE RECOVERED",
        "coin.recovered.body": "node is back online and responding.",
        "coin.recovered.footer": "Node recovered at {time}",
        "coin.change.title": "🔄 COIN CHANGE DETECTED: {old} → {new}",
        "coin.change.body": "Pool configuration changed. Sentinel has automatically switched monitoring.",
        "coin.change.footer": "Auto-detected at {time}",
        "coin.added.title": "➕ COIN ADDED: {coin_emoji} {coin}",
        "coin.added.body": "has been added to the pool.",
        "coin.added.footer": "Coin added at {time}",
        "coin.removed.title": "➖ COIN REMOVED: {coin_emoji} {coin}",
        "coin.removed.body": "has been removed from the pool.",
        "coin.removed.footer": "Coin removed at {time}",
        # Quotes
        "quotes": [
            "Sentinel online \u2022 Watching the grid",
            "Hash rate is truth, choom",
            "Flatline the network \u2022 Capture the block",
            "Power in, sats out \u2022 That's the deal",
            "Your rigs, your blocks \u2022 Stay frosty",
            "Tick tock next block \u2022 Sentinel watching",
            "Code is law \u2022 Miners are validators",
            "Eyes on the network \u2022 Always scanning",
            "Running hot \u2022 Mining cold hard crypto",
            "Not your pool, not your blocks",
            "Burn silicon \u2022 Stack sats \u2022 Never stop",
            "The blockchain never sleeps \u2022 Neither do we",
            "Zero downtime \u2022 Maximum chaos",
            "Nonces don't find themselves, choom",
            "Every hash is a bullet \u2022 Aim for the block",
            "Wake up, samurai \u2022 We have blocks to mine",
            "The grid remembers \u2022 Every share counts",
            "Solo miners die standing \u2022 Never kneeling",
            "Overclock your dreams \u2022 Underclock your fears",
            "Blocks or flatline \u2022 No in-between",
            "Proof of work \u2022 Proof of grit",
            "Silicon soldiers \u2022 Fighting for blocks",
            "Your hashrate speaks louder than words",
            "The network is the enemy \u2022 The block is the prize",
            "Stay paranoid \u2022 Stay profitable",
        ],
    },
    "professional": {
        # Startup
        "startup.banner": "SENTINEL ONLINE",
        "startup.status": "All systems operational",
        "startup.footer": "Monitoring {total} miners \u2022 {coin_name} Solo Pool",
        # Block found
        "block.title": "\u2705 {coin} BLOCK FOUND",
        "block.banner": "\u2705 BLOCK FOUND \u2705",
        "block.hero": "\u2705 **Solo block found on the network.**",
        "block.sub": "\U0001f4e6 *Block accepted by the network.*",
        "block.found_by": "found the block",
        "block.footer": "Solo mine confirmed",
        # Block orphaned
        "orphaned.title": "\u26a0\ufe0f {coin} BLOCK ORPHANED",
        "orphaned.banner_emoji": "\u26a0\ufe0f",
        "orphaned.flavor": "A previously found block has been orphaned.",
        # Miner offline
        "offline.title": "\U0001f6a8 MINER OFFLINE",
        "offline.banner": "MINER OFFLINE",
        "offline.footer": "Monitoring for recovery",
        "offline.footer_noip": "Monitoring for recovery \u2022 Check network/power",
        # Miner online
        "online.title": "\u2705 MINER RECOVERED",
        "online.banner": "MINER RECOVERED",
        "online.flavor": "has recovered and resumed mining.",
        "online.footer": "Connection restored successfully",
        # Restart
        "restart.title": "\U0001f504 AUTO-RESTART",
        "restart.success_banner": "Restart signal sent",
        "restart.fail_banner": "Restart attempt failed",
        "restart.footer": "Automated recovery system",
        # Zombie
        "zombie.title": "\u26a0\ufe0f IDLE MINER ALERT",
        "zombie.banner": "\u26a0\ufe0f IDLE MINER DETECTED \u26a0\ufe0f",
        "zombie.flavor": "is connected but not submitting valid work",
        "zombie.footer": "Auto-restart triggered \u2022 Investigate root cause",
        # Degradation
        "degradation.title": "\U0001f4c9 HASHRATE DEGRADATION",
        "degradation.footer": "Performance monitoring active",
        # Orphan spike
        "orphan_spike.banner_emoji": "\u26a0\ufe0f",
        "orphan_spike.title": "\u26a0\ufe0f BLOCK ORPHAN SPIKE",
        # Opportunity - crash
        "opportunity.crash.title": "NETWORK HASHRATE DECREASE",
        "opportunity.crash.banner": "\U0001f3af OPPORTUNITY WINDOW OPEN \U0001f3af",
        "opportunity.crash.flavor": "**{severity} DROP.** Network hashrate has decreased.",
        "opportunity.crash.footer": "\U0001f300 {coin} \u2022 Network down {drop_pct:.0f}% \u2022 Mining conditions improved",
        # Opportunity - high odds
        "opportunity.high_odds.title": "FAVORABLE MINING CONDITIONS",
        "opportunity.high_odds.banner": "\U0001f4ca FAVORABLE CONDITIONS \U0001f4ca",
        "opportunity.high_odds.flavor": "\U0001f4ca **{odds_status} conditions detected.** Mining odds are favorable.",
        "opportunity.high_odds.footer": "\U0001f300 {coin} \u2022 {time} \u2022 Mining conditions favorable",
        # Pool hashrate drop
        "pool_drop.title": "\U0001f4c9 FLEET HASHRATE DROP",
        "pool_drop.banner": "FLEET HASHRATE DROP",
        "pool_drop.body": "\u26a0\ufe0f **{severity}** - Pool hashrate dropped **{drop_pct:.1f}%**",
        "pool_drop.footer": "\U0001f527 Investigate fleet status",
        # Mode switch
        "mode_switch.solo_to_multi": "\U0001f504 MODE SWITCH: SOLO → MULTI-COIN",
        "mode_switch.multi_to_solo": "\U0001f504 MODE SWITCH: MULTI → SOLO ({coin})",
        "mode_switch.footer": "Mode changed at {time}",
        # Consolidated coin config
        "coin_config.mode_switch": "\U0001f504 POOL CONFIGURATION: {old} → {new}",
        "coin_config.coins_changed": "\U0001f504 POOL CONFIGURATION: COINS UPDATED",
        "coin_config.coins_added": "➕ POOL CONFIGURATION: +{count} COIN{plural}",
        "coin_config.coins_removed": "➖ POOL CONFIGURATION: -{count} COIN{plural}",
        "coin_config.footer": "Configuration changed at {time}",
        # Update notification
        "update.title": "\U0001f504 SPIRAL POOL UPDATE AVAILABLE",
        "update.body": "A new version of Spiral Pool is available. Run the upgrade when ready.",
        "update.footer": "Sentinel Update Check \u2022 Run upgrade manually when ready",
        # Best share
        "best_share.title": "\U0001f3c6 NEW BEST SHARE",
        "best_share.flavor": "New highest difficulty share submitted.",
        "best_share.footer": "New personal best share difficulty",
        # Miner reboot
        "reboot.title": "\u26a1 MINER REBOOT",
        "reboot.flavor": "has rebooted",
        "reboot.footer": "Monitoring \u2022 Miner recovered",
        # Power event
        "power_event.title": "\u26a1 FLEET POWER EVENT",
        "power_event.footer": "Check power infrastructure \u2022 Multiple rigs affected",
        # Orphan spike
        "orphan_spike.footer": "Block revenue lost \u2022 Check node propagation",
        # Revenue decline
        "revenue_decline.title": "\U0001f4c9 REVENUE PACE DECLINING",
        # HA - VIP Change
        "ha.vip_change.title": "🔄 HA VIP REASSIGNED",
        "ha.vip_change.banner": "VIP REASSIGNED",
        "ha.vip_change.body": "The cluster Virtual IP has been reassigned.",
        "ha.vip_change.footer": "VIP changed • Miner connections redirected",
        # HA - Failover
        "ha.failover.title": "⚠️ HA FAILOVER IN PROGRESS",
        "ha.failover.banner": "FAILOVER IN PROGRESS",
        "ha.failover.body": "A node failure has triggered automatic failover. Services may briefly be unavailable.",
        "ha.failover.footer": "Failover active • Services transitioning",
        # HA - Degraded
        "ha.degraded.title": "🔴 HA CLUSTER DEGRADED",
        "ha.degraded.banner": "CLUSTER DEGRADED",
        "ha.degraded.body": "One or more nodes may be offline. Cluster redundancy is reduced.",
        "ha.degraded.footer": "Cluster degraded • Investigate node status",
        # HA - Recovered
        "ha.recovered.title": "✅ HA CLUSTER RECOVERED",
        "ha.recovered.banner": "CLUSTER RECOVERED",
        "ha.recovered.body": "The HA cluster has returned to normal operation.",
        "ha.recovered.footer": "Cluster recovered • All nodes operational",
        # HA - State Change (generic)
        "ha.state_change.title": "🔄 HA STATE: {state}",
        "ha.state_change.body": "HA cluster state changed from `{old}` to `{new}`.",
        # HA - Replica Drop
        "ha.replica_drop.title": "🔗 HA REPLICA LOST",
        "ha.replica_drop.banner": "REPLICA LOST",
        "ha.replica_drop.body": "HA replica count decreased. Cluster redundancy has been reduced.",
        "ha.replica_drop.footer": "Cluster redundancy reduced • Check replica nodes",
        # HA - Promoted
        "ha.promoted.title": "👑 NODE PROMOTED TO MASTER",
        "ha.promoted.banner": "NODE PROMOTED",
        "ha.promoted.body": "This node has been promoted to master. All services starting.",
        "ha.promoted.footer": "Node promoted • Services starting",
        # HA - Demoted
        "ha.demoted.title": "🔄 NODE DEMOTED TO BACKUP",
        "ha.demoted.banner": "NODE DEMOTED",
        "ha.demoted.body": "This node has been demoted to backup. Managed services stopping.",
        "ha.demoted.footer": "Node demoted • Services stopped",
        # HA - Replication Lag
        "ha.replication_lag.title": "⏱️ REPLICATION LAG WARNING",
        "ha.replication_lag.banner": "REPLICATION LAG",
        "ha.replication_lag.body": "Database replication is falling behind the primary. Failover readiness may be impacted.",
        "ha.replication_lag.footer": "Replication lag detected • Monitor database I/O",
        # HA - Resync Estimate
        "ha.resync.title": "🔄 POST-FAILOVER RESYNC",
        "ha.resync.banner": "RESYNC IN PROGRESS",
        "ha.resync.body": "Backup node is resyncing after outage.",
        "ha.resync.footer": "Resync in progress • Estimated time based on throughput",
        # HA - Mode Toggle
        "ha.enabled.title": "✅ HA MODE ACTIVATED",
        "ha.enabled.body": "High availability has been enabled. The cluster is now operational.",
        "ha.enabled.footer": "HA cluster active • Failover protection enabled",
        "ha.disabled.title": "🔴 HA MODE DISABLED",
        "ha.disabled.body": "High availability has been disabled. Operating in single-node mode.",
        "ha.disabled.footer": "HA disabled • Single-node operation",
        # Infrastructure
        "infra.circuit_breaker.title": "🔴 CIRCUIT BREAKER OPEN",
        "infra.circuit_breaker.banner": "CIRCUIT BREAKER OPEN",
        "infra.circuit_breaker.body": "Shares are being rejected. Miners are hashing but work is not being counted.",
        "infra.circuit_breaker.footer": "CRITICAL: Pool rejecting shares • Check pool infrastructure immediately",
        "infra.backpressure.title": "BACKPRESSURE {level}",
        "infra.backpressure.banner": "BACKPRESSURE {level}",
        "infra.backpressure.body": "Pool buffer under pressure. Shares may be dropped if buffer overflows.",
        "infra.backpressure.footer": "Pool buffer under pressure • Check pool I/O capacity",
        "infra.wal_errors.title": "💾 DATABASE WRITE ERRORS",
        "infra.wal_errors.banner": "DATABASE WRITE ERRORS",
        "infra.wal_errors.body": "Accepted shares may not be persisting to disk. Possible silent data loss.",
        "infra.wal_errors.footer": "CRITICAL: Share data may be lost • Check disk and database immediately",
        "infra.zmq_disconnected.title": "📡 ZMQ CONNECTION LOST",
        "infra.zmq_disconnected.banner": "ZMQ CONNECTION LOST",
        "infra.zmq_disconnected.body": "Block notifications are not being received in real-time.",
        "infra.zmq_disconnected.footer": "Real-time block detection impaired • Check ZMQ endpoint",
        "infra.zmq_stale.title": "📡 ZMQ STALE",
        "infra.zmq_stale.banner": "ZMQ MESSAGE STALE",
        "infra.zmq_stale.body": "Block detection may be delayed. Falling back to RPC polling.",
        "infra.zmq_stale.footer": "Block notification delay • Check ZMQ connectivity",
        "infra.alert.title": "{prefix} {icon} Infrastructure Alert",
        "infra.alert.footer": "Alert type: {alert_type} | Severity: {severity}",
        "infra.api.title": "{prefix} {title}{coin}",
        "infra.api.footer": "Pool API Sentinel • {alert_type}",
        # Reports & Status
        # HA Status
        "ha.status.title": "🔄 HA Cluster Status",
        "ha.status.body": "Node `{node}` is **{role}** in the cluster",
        "ha.status.footer": "VIP: {vip}",
        # Infrastructure Health
        "infra.health.title": "🏗️ Infrastructure Health — {status}",
        "infra.health.body": "Backend metrics and operational status",
        "infra.health.footer": "🌀 Spiral Sentinel Infrastructure Monitor",
        # 6-Hour / Daily Intel Report
        "report.title_daily": "{coin_emoji} DAILY INTEL REPORT",
        "report.title_goodnight": "🛌😴💤 GOOD NIGHT — 6-HOUR INTEL REPORT",
        "report.title_morning": "🌅🌞☕ GOOD MORNING — 6-HOUR INTEL REPORT",
        "report.title_default": "{coin_emoji} 6-HOUR INTEL REPORT",
        "report.footer": "🌀 Spiral Sentinel v{version} • Next report in {next_report}",
        # Weekly Summary
        "weekly.title": "📈 WEEKLY SUMMARY",
        "weekly.banner": "📈 WEEKLY SUMMARY 📈",
        "weekly.footer": "🌀 Spiral Sentinel v{version}",
        # Monthly Earnings
        "monthly.title": "📅 MONTHLY EARNINGS",
        "monthly.banner": "💰 MONTHLY EARNINGS 💰",
        "monthly.footer": "🌀 Spiral Sentinel v{version} • Monthly maintenance recommended",
        # Maintenance Reminder
        "maintenance.title": "🔧 MONTHLY MAINTENANCE REMINDER",
        "maintenance.banner": "🔧 MONTHLY MAINTENANCE REMINDER",
        "maintenance.intro": "Time for routine maintenance!",
        "maintenance.footer": "🌀 Spiral Sentinel v{version} • Healthy miners = happy mining!",
        # Quarterly Report
        "quarterly.title": "📅 {quarter} QUARTERLY REPORT",
        "quarterly.body": "*Quarter ending {date}*",
        # Special Date
        "special.title": "{emoji} {name} MINING REPORT",
        "special.spring_equinox": "🌸 A new season begins. The network mines on — consistent, indifferent to the calendar.",
        "special.summer_solstice": "☀️ The longest day of the year. Peak solar generation for those supplementing with renewables.",
        "special.autumn_equinox": "🍂 Cooling temperatures ahead — favorable conditions for mining hardware efficiency.",
        "special.winter_solstice": "❄️ The shortest day of the year. Cold ambient air improves cooling — optimal mining conditions.",
        "special.new_years": "🎆 A new year of mining begins. The blockchain continues its unbroken sequence — block after block.",
        "special.christmas": "🎄 Happy holidays. Mining operations continue uninterrupted — consistency is the foundation of solo mining.",
        "special.easter": "🐣 Wishing you a restful holiday. The pool continues mining — no intervention required.",
        "special.good_friday": "✝️ A day of observance. Pool operations remain fully automated and operational.",
        "special.canada_day": "🍁 Happy Canada Day. Canadian hydroelectric power remains among the most cost-effective for mining globally.",
        "special.victoria_day": "👑 Happy Victoria Day. A good opportunity for scheduled maintenance during the long weekend.",
        "special.civic_holiday": "🏖️ Civic Holiday. Consider scheduling thermal paste replacement or fan cleaning during downtime.",
        "special.labour_day": "⚒️ Happy Labour Day. Automated mining — reliable returns without manual intervention.",
        "special.thanksgiving_ca": "🦃 Happy Thanksgiving. Consistent uptime and low difficulty are worth being grateful for.",
        "special.remembrance_day": "🌺 Lest we forget. Mining operations continue — the network never pauses.",
        "special.boxing_day": "🎁 Happy Boxing Day. A good time to review hardware deals for fleet expansion.",
        "special.family_day": "👨‍👩‍👧‍👦 Happy Family Day. Pool automation ensures operations continue while you enjoy the holiday.",
        "special.indigenous_day": "🪶 National Indigenous Peoples Day. Honouring the heritage, cultures, and contributions of Indigenous peoples.",
        "special.truth_reconciliation": "🧡 National Day for Truth and Reconciliation. A day to remember, reflect, and acknowledge the path forward.",
        "special.independence_day": "🦅 Happy Independence Day. Decentralized mining embodies the principle of financial self-sovereignty.",
        "special.mlk_day": "✊ Honoring Dr. King's legacy. Proof-of-work is permissionless — open to all participants equally.",
        "special.presidents_day": "🏛️ Happy Presidents' Day. The blockchain operates beyond any single authority — by design.",
        "special.memorial_day": "🎖️ Honoring those who served. Your mining fleet serves the network with the same steadfast reliability.",
        "special.labor_day": "⚒️ Happy Labor Day. Automated mining — reliable returns without manual intervention.",
        "special.columbus_day": "🚢 Happy Columbus Day. Consistent exploration of the hash space yields results over time.",
        "special.thanksgiving_us": "🦃 Happy Thanksgiving. Consistent uptime and favorable network conditions are worth appreciating.",
        "special.juneteenth": "✊🏿 Juneteenth. Celebrating freedom and the ongoing pursuit of equality. The blockchain is permissionless by design.",
        "special.veterans_day": "🎖️ Honoring those who served. The network's security depends on miners who show up — every day.",
        "special.fallback": "🌟 Wishing you a good holiday. Mining operations continue as scheduled.",
        # Consolidated Report
        "consolidated.title_suffix": "INTEL REPORT",
        # Trend Analysis
        "trend.title": "📊 {coin} TREND ANALYSIS",
        # Difficulty Report
        "difficulty.title": "🎯 {coin} DIFFICULTY REPORT{algo}",
        # Achievement
        "achievement.title": "🏆 ACHIEVEMENT UNLOCKED!",
        "achievement.footer": "🌀 Spiral Sentinel v{version} • Achievement System",
        # Miner/Hardware
        "temp.warning_title": "🌡️ THERMAL WARNING",
        "temp.critical_title": "🔥 THERMAL CRITICAL",
        "temp.warning_banner": "Elevated temperature detected",
        "temp.critical_banner": "OVERHEATING DETECTED",
        "temp.warning_footer": "Monitor closely • Check ambient temperature and airflow",
        "temp.critical_footer": "IMMEDIATE ACTION REQUIRED • Check fans and airflow",
        "thermal.title": "THERMAL SHUTDOWN",
        "thermal.banner": "THERMAL EMERGENCY",
        "thermal.stopped": "MINER HAS BEEN STOPPED",
        "thermal.failed": "COULD NOT STOP MINER — UNPLUG IMMEDIATELY",
        "thermal.footer": "CRITICAL: Manual intervention required to restore mining",
        "fan.title": "🌀 FAN FAILURE",
        "fan.banner": "FAN FAILURE DETECTED",
        "fan.body": "Check cooling system immediately.",
        "fan.footer": "Fan at 0 RPM while miner is running • Check cooling immediately",
        "hashboard.title": "🪫 HASHBOARD FAILURE",
        "hashboard.banner": "HASHBOARD FAILURE",
        "hashboard.footer": "Hardware failure detected • Board replacement may be required",
        "hw_error.title": "⚠️ HARDWARE ERROR RATE",
        "hw_error.banner": "HARDWARE ERROR RATE",
        "hw_error.body": "Rising hardware errors may indicate ASIC chip degradation.",
        "hw_error.footer": "Elevated HW error rate • Monitor for chip failure",
        "rejection.title": "📊 SHARE REJECTION SPIKE",
        "rejection.banner": "SHARE REJECTION SPIKE",
        "rejection.body": "Shares are being rejected at an abnormal rate.",
        "rejection.footer": "High rejection rate reduces effective hashrate • Check configuration",
        "url_mismatch.title": "🔒 STRATUM URL MISMATCH",
        "url_mismatch.banner": "STRATUM URL MISMATCH",
        "url_mismatch.body": "This may indicate firmware hijacking, misconfiguration, or failover activation.",
        "url_mismatch.footer": "SECURITY: Verify miner firmware and pool configuration",
        "chronic.title": "🔁 CHRONIC ISSUE DETECTED",
        "chronic.banner": "🔁 CHRONIC ISSUE 🔁",
        "chronic.body": "has a recurring issue that requires investigation.",
        "chronic.footer": "Manual inspection recommended",
        "excessive_restarts.title": "⚡ EXCESSIVE RESTARTS",
        "excessive_restarts.banner": "EXCESSIVE RESTARTS",
        "excessive_restarts.body": "Frequent reboots indicate hardware or power supply issues.",
        "excessive_restarts.footer": "Restart loop detected • Check power and cooling",
        # Performance
        "divergence.title": "📡 HASHRATE DIVERGENCE",
        "divergence.banner": "HASHRATE DIVERGENCE DETECTED",
        "divergence.footer": "Pool not receiving expected hashrate • Check network and share submission",
        "worker_drop.title": "👷 WORKER COUNT DROP",
        "worker_drop.banner": "WORKER COUNT DROP",
        "worker_drop.body": "Multiple workers may have disconnected.",
        "worker_drop.footer": "Workers disconnected • Check fleet connectivity",
        "share_loss.title": "📉 SHARE LOSS RATE",
        "share_loss.banner": "SHARE LOSS RATE ELEVATED",
        "share_loss.body": "Shares are being lost between miner and pool accounting.",
        "share_loss.footer": "Shares being dropped • Check pool infrastructure",
        "notify_mode.title": "📡 BLOCK NOTIFY MODE",
        "notify_mode.banner": "BLOCK NOTIFY MODE CHANGE",
        "notify_mode.footer": "Block detection method changed",
        # Financial
        "sats_surge.title": "📈 {coin_emoji} {coin_name} SAT SURGE ALERT",
        "sats_surge.body": "sat value has increased significantly against BTC.",
        "sats_surge.footer": "Sats Surge Tracker",
        "price_crash.title": "📉 {coin} PRICE CRASH",
        "price_crash.body": "has dropped significantly in the last hour.",
        "price_crash.footer": "Price crash detection • {threshold}%+ drop threshold",
        "payout.title": "💰 {coin} PAYOUT RECEIVED",
        "payout.body": "payout detected.",
        "payout.footer": "Payout confirmed on-chain",
        "wallet_drop.title": "🚨 {coin} WALLET BALANCE DROP",
        "wallet_drop.body": "wallet balance decreased unexpectedly.",
        "wallet_drop.footer": "Verify wallet transaction history",
        "missing_payout.title": "⚠️ {coin} MISSING PAYOUT",
        "missing_payout.body": "wallet balance has not changed in {days} days.",
        "missing_payout.footer": "No balance change in {days}d",
        # Coin config
        "coin.node_down.title": "🔴 NODE DOWN: {coin_emoji} {coin}",
        "coin.node_down.banner": "{coin_name} NODE UNREACHABLE",
        "coin.node_down.body": "node is offline. The pool cannot communicate with this blockchain node.",
        "coin.node_down.footer": "Node down at {time}",
        "coin.sync_behind.title": "⏳ SYNC BEHIND: {coin_emoji} {coin}",
        "coin.sync_behind.banner": "{coin_name} node syncing",
        "coin.sync_behind.body": "Mining efficiency may be affected until the node catches up.",
        "coin.sync_behind.footer": "Sync issue at {time}",
        "coin.recovered.title": "✅ NODE RECOVERED: {coin_emoji} {coin}",
        "coin.recovered.banner": "{coin_name} NODE RECOVERED",
        "coin.recovered.body": "node is back online and responding.",
        "coin.recovered.footer": "Node recovered at {time}",
        "coin.change.title": "🔄 COIN CHANGE DETECTED: {old} → {new}",
        "coin.change.body": "Pool configuration changed. Sentinel has automatically switched monitoring.",
        "coin.change.footer": "Auto-detected at {time}",
        "coin.added.title": "➕ COIN ADDED: {coin_emoji} {coin}",
        "coin.added.body": "has been added to the pool.",
        "coin.added.footer": "Coin added at {time}",
        "coin.removed.title": "➖ COIN REMOVED: {coin_emoji} {coin}",
        "coin.removed.body": "has been removed from the pool.",
        "coin.removed.footer": "Coin removed at {time}",
        # Quotes
        "quotes": [
            "Sentinel online \u2022 Monitoring fleet",
            "Hashrate steady \u2022 Systems nominal",
            "Network scanning \u2022 All pools connected",
            "Fleet operational \u2022 Shares flowing",
            "Monitoring active \u2022 Standing by",
            "Systems checked \u2022 Mining steady",
            "Pool connected \u2022 Shares verified",
            "Uptime tracked \u2022 Performance logged",
            "Fleet status: operational",
            "Continuous monitoring in progress",
            "All miners reporting \u2022 No anomalies detected",
            "Share submission rate within normal parameters",
            "Block detection active \u2022 Polling nominal",
            "Fleet utilization optimal \u2022 No intervention required",
            "Network difficulty tracked \u2022 Adjustments logged",
            "Automated monitoring cycle complete",
            "All endpoints responsive \u2022 Latency nominal",
            "Mining operations proceeding as expected",
            "Health checks passed \u2022 All services green",
            "Performance baseline maintained \u2022 No deviations",
            "Scheduled scan complete \u2022 Fleet intact",
            "Operational metrics within tolerance",
            "Infrastructure stable \u2022 No alerts pending",
            "Monitoring interval complete \u2022 Status unchanged",
            "All nodes synchronized \u2022 Consensus verified",
        ],
    },
}


def theme(key, **kwargs):
    """Look up themed text by dot-key. Falls back: current → cyberpunk → raw key.
    Supports str.format(**kwargs) for dynamic values."""
    current = ALERT_THEMES.get(ALERT_THEME, ALERT_THEMES["cyberpunk"])
    text = current.get(key)
    if text is None:
        text = ALERT_THEMES["cyberpunk"].get(key, key)
    if kwargs:
        try:
            text = text.format(**kwargs)
        except (KeyError, IndexError):
            pass  # Return unformatted if kwargs don't match
    return text


def get_themed_quotes():
    """Return quote list for the active theme."""
    current = ALERT_THEMES.get(ALERT_THEME, ALERT_THEMES["cyberpunk"])
    return current.get("quotes", ALERT_THEMES["cyberpunk"]["quotes"])


def format_currency_value(prices, dgb_amount=None, show_dgb=True):
    """
    Format currency value based on user's report_currency preference.
    prices dict must have lowercase currency code keys.
    """
    if prices is None:
        prices = {}

    result = ""
    amount = dgb_amount or 0

    if show_dgb and dgb_amount is not None:
        result += f"💎 {dgb_amount:.2f} DGB\n"

    cur = get_currency_meta()
    fiat_val = amount * prices.get(cur["code"], 0)
    result += f"{cur['emoji']} {cur['symbol']}{fiat_val:.{cur['decimals']}f} {REPORT_CURRENCY}"

    return result.strip()

def format_price_per_dgb(prices):
    """Format coin price in user's preferred currency (works for any coin, not just DGB)"""
    cur = get_currency_meta()
    price = prices.get(cur["code"], 0)
    return f"{cur['emoji']} {cur['symbol']}{price:.6f}/{REPORT_CURRENCY}"

def get_preferred_price(prices):
    """Get price value in user's preferred currency (for calculations)"""
    cur = get_currency_meta()
    return prices.get(cur["code"], 0)

# Telegram configuration
TELEGRAM_BOT_TOKEN = CONFIG.get("telegram_bot_token", "")
TELEGRAM_CHAT_ID = CONFIG.get("telegram_chat_id", "")
# Auto-enable Telegram if credentials are provided (unless explicitly disabled)
# If telegram_enabled is not set (None), auto-enable when credentials present
# If telegram_enabled is explicitly set, respect that setting
_telegram_enabled_setting = CONFIG.get("telegram_enabled")
if _telegram_enabled_setting is None:
    # Not explicitly configured - auto-enable if credentials present
    TELEGRAM_ENABLED = bool(TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID)
else:
    # Explicitly configured - respect the setting (but still require credentials)
    TELEGRAM_ENABLED = _telegram_enabled_setting and TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID

# Audit #9: Telegram rate limiter to prevent burst flooding
_last_telegram_send = 0
_TELEGRAM_MIN_INTERVAL = 1.0  # minimum seconds between Telegram messages

# XMPP configuration
XMPP_JID = CONFIG.get("xmpp_jid", "")
XMPP_PASSWORD = CONFIG.get("xmpp_password", "")
XMPP_RECIPIENT = CONFIG.get("xmpp_recipient", "")
XMPP_USE_TLS = CONFIG.get("xmpp_use_tls", True)
XMPP_MUC = CONFIG.get("xmpp_muc", False)
# Auto-enable XMPP if credentials are provided (unless explicitly disabled)
_xmpp_enabled_setting = CONFIG.get("xmpp_enabled")
if _xmpp_enabled_setting is None:
    XMPP_ENABLED = XMPP_AVAILABLE and bool(XMPP_JID and XMPP_PASSWORD and XMPP_RECIPIENT)
else:
    XMPP_ENABLED = _xmpp_enabled_setting and XMPP_AVAILABLE and bool(XMPP_JID and XMPP_PASSWORD and XMPP_RECIPIENT)

# Warn if config says enabled but slixmpp is missing (user intent vs reality)
if CONFIG.get("xmpp_enabled") and not XMPP_AVAILABLE and bool(XMPP_JID and XMPP_PASSWORD and XMPP_RECIPIENT):
    logger.warning("XMPP enabled in config but slixmpp not installed. Run: pip install slixmpp")

# ntfy configuration
NTFY_URL   = CONFIG.get("ntfy_url", "").strip()
NTFY_TOKEN = CONFIG.get("ntfy_token", "").strip()
# Auto-enable if URL is configured
NTFY_ENABLED = bool(NTFY_URL)

# SMTP / email configuration
SMTP_HOST     = CONFIG.get("smtp_host", "").strip()
SMTP_PORT     = int(CONFIG.get("smtp_port", 587))
SMTP_USERNAME = CONFIG.get("smtp_username", "").strip()
SMTP_PASSWORD = CONFIG.get("smtp_password", "")
SMTP_FROM     = CONFIG.get("smtp_from", "").strip() or SMTP_USERNAME
SMTP_TO_RAW   = CONFIG.get("smtp_to", "").strip()
SMTP_TO       = [addr.strip() for addr in SMTP_TO_RAW.split(",") if addr.strip()]
SMTP_USE_TLS  = CONFIG.get("smtp_use_tls", True)
_smtp_enabled_setting = CONFIG.get("smtp_enabled")
if _smtp_enabled_setting is None:
    SMTP_ENABLED = bool(SMTP_HOST and SMTP_USERNAME and SMTP_PASSWORD and SMTP_TO)
else:
    SMTP_ENABLED = bool(_smtp_enabled_setting) and bool(SMTP_HOST and SMTP_USERNAME and SMTP_PASSWORD and SMTP_TO)

# Telegram bot commands configuration
TELEGRAM_COMMANDS_ENABLED = (
    TELEGRAM_ENABLED and
    bool(CONFIG.get("telegram_commands_enabled", True))
)
_telegram_cmd_offset = 0   # getUpdates offset for long-poll dedup
_tg_last_cmd_ts: float = 0.0  # rate-limit: earliest time next command is accepted

# Sentinel health endpoint configuration
SENTINEL_HEALTH_ENABLED = bool(CONFIG.get("sentinel_health_enabled", True))
SENTINEL_HEALTH_PORT    = int(CONFIG.get("sentinel_health_port", 9191))
SENTINEL_START_TIME     = time.time()   # Process start timestamp for uptime reporting
_health_state_ref       = None          # Set in monitor_loop after state is initialized

# Default miners - empty for fresh installations
# Configure your miners via: spiralpool-scan, spiralpool-config, or edit miners.json
DEFAULT_MINERS = {
    # ═══════════════════════════════════════════════════════════════════════════════
    # SHA256 MINERS - AxeOS/ESP-Miner HTTP API (port 80)
    # ═══════════════════════════════════════════════════════════════════════════════
    "axeos": [],           # Generic AxeOS devices (fallback for unknown AxeOS)
    "bitaxe": [],          # BitAxe family: BitAxe Ultra, BitAxe Supra, BitAxe Gamma, etc.
    "nmaxe": [],           # NMAxe specifically (~500 GH/s, BM1366)
    "nerdaxe": [],         # NerdAxe (original single-ASIC, ~400 GH/s)
    "nerdqaxe": [],        # NerdQAxe++ (quad-ASIC, ~1.6 TH/s)
    "nerdoctaxe": [],      # NerdOctaxe (octa-ASIC, ~3.2 TH/s)
    "qaxe": [],            # QAxe (quad-ASIC variant)
    "qaxeplus": [],        # QAxe+ (enhanced quad-ASIC)
    "luckyminer": [],      # Lucky Miner LV06/LV07/LV08 (AxeOS HTTP API)
    "jingleminer": [],     # Jingle Miner BTC Solo Pro/Lite (AxeOS HTTP API)
    "zyber": [],           # Zyber 8G/8GP/8S TinyChipHub (AxeOS HTTP API)
    # ═══════════════════════════════════════════════════════════════════════════════
    # SHA256 MINERS - CGMiner API (port 4028)
    # ═══════════════════════════════════════════════════════════════════════════════
    "avalon": [],          # Avalon Nano/ASIC devices (Avalon Nano 3S, etc.)
    "antminer": [],        # Bitmain Antminer S19/S21/T21 (SHA256)
    "whatsminer": [],      # MicroBT Whatsminer M30/M50/M60/M63 (SHA256)
    "innosilicon": [],     # Innosilicon A10/A11/T3 (SHA256)
    "futurebit": [],       # FutureBit Apollo/Apollo II (SHA256)
    "canaan": [],          # Canaan AvalonMiner A12/A13/A14 series
    "ebang": [],           # Ebang Ebit E12/E12+ (SHA256)
    "gekkoscience": [],    # GekkoScience Compac F, NewPac, R606 (CGMiner API)
    "ipollo": [],          # iPollo V1/V1 Mini/G1 (CGMiner API)
    "epic": [],            # ePIC BlockMiner (CGMiner API)
    # ═══════════════════════════════════════════════════════════════════════════════
    # SCRYPT MINERS
    # ═══════════════════════════════════════════════════════════════════════════════
    "antminer_scrypt": [], # Bitmain Antminer L7/L9 (Scrypt, CGMiner API port 4028)
    "goldshell": [],       # Goldshell Mini DOGE/LT5/LT6 (Scrypt, HTTP API port 80)
    "hammer": [],          # PlebSource Hammer Miner (Scrypt, AxeOS-style HTTP API)
    "elphapex": [],        # Elphapex DG1/DG Home (Scrypt, CGMiner API port 4028)
    "esp32miner": [],      # ESP32 Miner (ESP32-based solo miner)
    # ═══════════════════════════════════════════════════════════════════════════════
    # CUSTOM FIRMWARE - REST API (manual config only, not auto-detected by scan)
    # ═══════════════════════════════════════════════════════════════════════════════
    "braiins": [],         # BraiinsOS/BOS+ on Antminers (REST API port 80)
    "vnish": [],           # Vnish firmware on Antminers (REST API port 80 + CGMiner on 4028)
    "luxos": [],           # LuxOS firmware on Antminers (CGMiner API port 4028)
}

# Unified miner database file (shared with dashboard and config tools)
# Primary location is /spiralpool/data/ (shared between admin and service users)
MINER_DB_FILE = SHARED_DATA_DIR / "miners.json"

def load_miners():
    """Load miners from the unified miner database.

    Database format:
    {
        "miners": {
            "192.168.1.100": {
                "type": "nmaxe",
                "nickname": "BitAxe-Office",
                "added": "2025-01-15T10:30:00Z",
                "last_seen": "2025-01-15T12:00:00Z"
            },
            ...
        },
        "by_type": {
            "nmaxe": ["192.168.1.100"],
            "nerdqaxe": [],
            ...
        }
    }

    Returns format expected by monitoring code:
    {
        "nmaxe": [{"name": "BitAxe-Office", "ip": "192.168.1.100"}, ...],
        "avalon": [{"name": "Avalon-Nano3s", "ip": "192.168.1.14", "port": 4028}, ...],
        ...
    }
    """
    def convert_to_miner_objects(db):
        """Convert database entries to miner objects with name, ip, port, etc."""
        result = {k: [] for k in DEFAULT_MINERS.keys()}
        miners_info = db.get("miners", {})
        by_type = db.get("by_type", {})

        # Process each miner type
        for mtype, items in by_type.items():
            # axeos is now a proper type - no longer normalize to nmaxe
            if mtype not in result:
                continue

            for item in items:
                # Handle both formats:
                # 1. Plain IP string: "192.168.1.100"
                # 2. Miner object dict: {"ip": "192.168.1.100", "name": "BitAxe-1", ...}
                if isinstance(item, dict):
                    # Already a miner object - use it directly but ensure required fields
                    ip = item.get("ip", "")
                    if not ip:
                        continue
                    miner_obj = {
                        "ip": ip,
                        "name": item.get("name") or item.get("nickname") or ip,
                        "port": item.get("port", 4028),
                    }
                    # Preserve optional fields
                    for key in ["fallback_ths", "fallback_ghs", "watts"]:
                        if item.get(key):
                            miner_obj[key] = item[key]
                else:
                    # Plain IP string - look up metadata
                    ip = str(item)
                    info = miners_info.get(ip, {})
                    miner_obj = {
                        "ip": ip,
                        "name": info.get("nickname") or ip,
                        "port": info.get("port", 4028),
                    }
                    # Add optional fields if present
                    if info.get("fallback_ths"):
                        miner_obj["fallback_ths"] = info["fallback_ths"]
                    if info.get("fallback_ghs"):
                        miner_obj["fallback_ghs"] = info["fallback_ghs"]
                    if info.get("watts"):
                        miner_obj["watts"] = info["watts"]

                # Auto-correct type mismatch: if the individual miner record in
                # miners_info has a different type than the by_type key, trust the
                # record's type. This fixes miners listed under the wrong by_type key
                # (e.g., ESP32 miner under "axeos" after a dashboard sync bug).
                target_type = mtype
                actual_ip = miner_obj["ip"]
                recorded_type = miners_info.get(actual_ip, {}).get("type", "")
                if recorded_type and recorded_type != mtype and recorded_type in result:
                    target_type = recorded_type
                    logger.info(f"Auto-corrected miner type for {miner_obj['name']} ({actual_ip}): "
                                f"by_type says '{mtype}' but record says '{recorded_type}', using '{recorded_type}'")

                result[target_type].append(miner_obj)

        return result

    # First try the unified database
    if MINER_DB_FILE.exists():
        try:
            with open(MINER_DB_FILE) as f:
                db = json.load(f)
                miners = convert_to_miner_objects(db)
                total = sum(len(v) for v in miners.values())
                if total > 0:
                    logger.info(f"Loaded {total} miners from unified database: {MINER_DB_FILE}")
                    return miners
                # BUGFIX: If miners.json exists but has 0 miners, fall through to
                # dashboard_config.json.  sync_miners_to_sentinel() had a bug that
                # prevented miners from ever reaching miners.json, so valid miners
                # may only exist in dashboard_config.json.
                logger.info("Unified database has 0 miners, checking dashboard config fallback...")
        except (json.JSONDecodeError, IOError, OSError) as e:
            logger.warning(f"Could not load miner database: {e}")

    # Fallback: check dashboard config for devices
    dashboard_configs = [
        INSTALL_DIR / "dashboard" / "data" / "dashboard_config.json",  # Production
    ]

    dashboard_config = None
    for cfg_path in dashboard_configs:
        if cfg_path.exists():
            dashboard_config = cfg_path
            logger.debug(f"Found dashboard config at: {cfg_path}")
            break

    if dashboard_config and dashboard_config.exists():
        try:
            with open(dashboard_config) as f:
                cfg = json.load(f)
                devices = cfg.get("devices", {})
                # Convert device IPs to miner objects
                by_type = {k: [] for k in DEFAULT_MINERS.keys()}
                # Load ALL device types from dashboard config
                for dtype in DEFAULT_MINERS.keys():
                    if dtype in devices:
                        by_type[dtype] = devices[dtype]
                miners = convert_to_miner_objects({"by_type": by_type, "miners": {}})
                total = sum(len(v) for v in miners.values())
                logger.info(f"Loaded {total} miners from dashboard config: {dashboard_config}")
                return miners
        except (json.JSONDecodeError, IOError, OSError) as e:
            logger.warning(f"Could not load dashboard config: {e}")

    logger.warning("No miner configuration found - using empty defaults")
    return DEFAULT_MINERS.copy()

def _atomic_json_save(filepath, data, indent=None):
    """Write JSON data atomically using temp + fsync + rename.

    Prevents data corruption if the process crashes mid-write.
    The rename operation is atomic on POSIX filesystems.
    """
    import tempfile, shutil
    filepath = Path(filepath) if not isinstance(filepath, Path) else filepath
    try:
        filepath.parent.mkdir(parents=True, exist_ok=True)
    except (PermissionError, OSError):
        pass  # Directory may already exist; mkstemp below will give clear error if not
    temp_fd, temp_path = tempfile.mkstemp(
        suffix='.tmp', prefix=filepath.stem + '_', dir=str(filepath.parent)
    )
    try:
        with os.fdopen(temp_fd, 'w') as f:
            json.dump(data, f, indent=indent)
            f.flush()
            os.fsync(f.fileno())
        shutil.move(temp_path, str(filepath))
    except Exception as e:
        try:
            os.unlink(temp_path)
        except OSError:
            pass
        logger.error(f"Atomic JSON save failed for {filepath}: {e}")
        # Don't re-raise — file save failure should not crash the monitor

def load_miner_database():
    """Load the full miner database with nicknames and metadata."""
    if MINER_DB_FILE.exists():
        try:
            with open(MINER_DB_FILE) as f:
                return json.load(f)
        except (json.JSONDecodeError, IOError, OSError):
            pass
    return {"miners": {}, "by_type": DEFAULT_MINERS.copy()}

def save_miner_database(db):
    """Save the miner database atomically (temp + fsync + rename)."""
    _atomic_json_save(MINER_DB_FILE, db, indent=2)

def get_miner_nickname(ip):
    """Get nickname for a miner from the database."""
    db = load_miner_database()
    miner = db.get("miners", {}).get(ip, {})
    return miner.get("nickname", "")

def set_miner_nickname(ip, nickname):
    """Set nickname for a miner in the database."""
    db = load_miner_database()
    if "miners" not in db:
        db["miners"] = {}
    if ip not in db["miners"]:
        db["miners"][ip] = {"type": "unknown", "added": utc_ts()}
    db["miners"][ip]["nickname"] = nickname
    save_miner_database(db)

MINERS = load_miners()
MINER_DB = load_miner_database()

# Track Avalon/Canaan display names discovered during hashrate collection.
# This handles cases where a miner is configured by IP but reports a different
# worker name via stratum - the display name won't match the config name.
_avalon_display_names = set()

def is_avalon_miner(name, device_model=None):
    """Check if a miner is an Avalon/Canaan device.

    Avalon devices are personal heaters - high temperatures are expected and normal.
    Temperature alerts should be skipped for these devices.

    Checks:
    1. Miner display name discovered during hashrate collection (handles IP->worker name mapping)
    2. Miner name in "avalon" or "canaan" config categories
    3. Device model contains avalon/canaan keywords (for pool-detected devices)
    """
    # Check display names discovered during hashrate collection
    # This handles IP-configured miners that report different worker names via stratum
    if name in _avalon_display_names:
        return True

    # Check if in avalon or canaan miner categories
    avalon_miners = MINERS.get("avalon", []) + MINERS.get("canaan", [])
    if any(m.get("name") == name for m in avalon_miners):
        return True

    # Check device model for Avalon/Canaan keywords (case-insensitive)
    if device_model:
        model_lower = device_model.lower()
        if any(kw in model_lower for kw in ["avalon", "canaan", "nano 3", "nano3"]):
            return True

    return False

# Reload trigger file - dashboard creates this to signal Sentinel to reload miners
MINER_RELOAD_TRIGGER = SHARED_DATA_DIR / ".reload_miners"
# P2 AUDIT FIX: Reload ACK file - Sentinel writes this to confirm reload completed
MINER_RELOAD_ACK = SHARED_DATA_DIR / ".reload_ack"
_last_miner_db_mtime = 0  # Track miners.json modification time

def reload_miners():
    """Reload miners from the database file.

    Called when the reload trigger file is detected or miners.json is modified.
    This allows hot-reloading of miner configuration without restarting Sentinel.

    P2 AUDIT FIX: Now writes an ACK file after successful reload so dashboard
    can verify the reload actually happened.
    """
    global MINERS, MINER_DB, _last_miner_db_mtime

    try:
        old_count = sum(len(v) for v in MINERS.values())
        MINERS = load_miners()
        MINER_DB = load_miner_database()
        new_count = sum(len(v) for v in MINERS.values())

        # Update mtime tracker
        if MINER_DB_FILE.exists():
            _last_miner_db_mtime = MINER_DB_FILE.stat().st_mtime

        logger.info(f"Reloaded miner configuration: {old_count} -> {new_count} miners")

        # Log details about what changed
        for mtype, miners in MINERS.items():
            if miners:
                logger.debug(f"  {mtype}: {len(miners)} miners")

        # P2 AUDIT FIX: Write ACK file to confirm reload completed
        # Dashboard can poll for this file to verify reload happened
        try:
            ack_data = {
                "timestamp": time.time(),
                "timestamp_iso": datetime.now(timezone.utc).isoformat(),
                "old_count": old_count,
                "new_count": new_count,
                "success": True,
                "sentinel_version": "V1.2.1-CONVERGENT_SPIRAL"
            }
            _atomic_json_save(MINER_RELOAD_ACK, ack_data)
            logger.debug(f"Wrote reload ACK: {MINER_RELOAD_ACK}")
        except (PermissionError, OSError) as e:
            logger.warning(f"Could not write reload ACK: {e}")
            # Non-fatal - reload still succeeded

        return True
    except Exception as e:
        logger.error(f"Failed to reload miners: {e}")

        # P2 AUDIT FIX: Write failure ACK so dashboard knows reload failed
        try:
            ack_data = {
                "timestamp": time.time(),
                "timestamp_iso": datetime.now(timezone.utc).isoformat(),
                "success": False,
                "error": "Failed to reload miner configuration",
                "sentinel_version": "V1.2.1-CONVERGENT_SPIRAL"
            }
            _atomic_json_save(MINER_RELOAD_ACK, ack_data)
        except (PermissionError, OSError):
            pass  # Best effort

        return False

def check_miner_reload_needed():
    """Check if miners need to be reloaded.

    Returns True if:
    - The reload trigger file exists (dashboard signaled a reload)
    - The miners.json file has been modified since last load
    """
    global _last_miner_db_mtime

    # Check for trigger file (highest priority - explicit reload request)
    if MINER_RELOAD_TRIGGER.exists():
        try:
            MINER_RELOAD_TRIGGER.unlink()  # Remove trigger file
            logger.info("Miner reload triggered by dashboard")
            return True
        except (PermissionError, OSError) as e:
            logger.warning(f"Could not remove reload trigger: {e}")
            return True  # Still reload even if we can't remove the trigger

    # Check if miners.json has been modified
    if MINER_DB_FILE.exists():
        try:
            current_mtime = MINER_DB_FILE.stat().st_mtime
            if current_mtime > _last_miner_db_mtime:
                logger.info("Miner database file modified, reloading...")
                return True
        except (OSError, IOError):
            pass

    return False

# Initialize mtime tracker
if MINER_DB_FILE.exists():
    try:
        _last_miner_db_mtime = MINER_DB_FILE.stat().st_mtime
    except (OSError, IOError):
        pass

# Cache for API-reported hostnames (populated during miner scans)
# Maps miner name/IP -> hostname reported by the miner's API
API_HOSTNAMES = {}

# Cache for worker names from stratum config (format: wallet.WorkerName)
# Used as fallback when hostname is not available
API_WORKER_NAMES = {}

# Cache for reverse DNS lookups (avoid repeated slow lookups)
RDNS_CACHE = {}

def cache_api_hostname(name_or_ip, hostname):
    """Cache hostname reported by miner API for display purposes."""
    if hostname and isinstance(hostname, str) and hostname.strip():
        # Store with both the name and extracted IP as keys
        clean_hostname = hostname.strip()
        API_HOSTNAMES[name_or_ip] = clean_hostname
        # Also store by IP if name contains IP
        ip_only = name_or_ip.split(':')[0] if ':' in name_or_ip else name_or_ip
        if ip_only != name_or_ip:
            API_HOSTNAMES[ip_only] = clean_hostname

def cache_api_worker_name(name_or_ip, worker_name):
    """Cache worker name from miner's stratum config for display fallback."""
    if worker_name and isinstance(worker_name, str) and worker_name.strip():
        clean_name = worker_name.strip()
        # Only use if it looks like a real name (not just IP or wallet fragment)
        if not clean_name.replace('.', '').isdigit() and len(clean_name) < 32:
            API_WORKER_NAMES[name_or_ip] = clean_name
            ip_only = name_or_ip.split(':')[0] if ':' in name_or_ip else name_or_ip
            if ip_only != name_or_ip:
                API_WORKER_NAMES[ip_only] = clean_name

def _try_reverse_dns(ip):
    """Try reverse DNS lookup with caching. Returns None on failure."""
    if ip in RDNS_CACHE:
        return RDNS_CACHE[ip]
    try:
        hostname = socket.gethostbyaddr(ip)[0]
        # Only use if it's a real hostname (not just the IP reversed)
        if hostname and not hostname.endswith('.in-addr.arpa') and hostname != ip:
            # Strip domain suffix for cleaner display
            short_name = hostname.split('.')[0]
            RDNS_CACHE[ip] = short_name
            return short_name
    except (socket.herror, socket.gaierror, socket.timeout, OSError):
        pass
    RDNS_CACHE[ip] = None
    return None

def get_miner_display_name(ip_or_name):
    """Get display name for a miner (nickname > API hostname > worker name > rDNS > IP)"""
    # Check the unified miner database first (user-configured nicknames)
    ip_only = ip_or_name.split(':')[0] if ':' in ip_or_name else ip_or_name

    miner_info = MINER_DB.get("miners", {}).get(ip_only, {})
    if miner_info.get("nickname"):
        return miner_info["nickname"]

    # Fallback to legacy nicknames file
    nicknames_file = DATA_DIR / "nicknames.json"
    if nicknames_file.exists():
        try:
            with open(nicknames_file) as f:
                nicknames = json.load(f)
                if ip_or_name in nicknames:
                    return nicknames[ip_or_name]
                if ip_only in nicknames:
                    return nicknames[ip_only]
        except (json.JSONDecodeError, KeyError, TypeError, AttributeError, OSError):
            pass

    # Check API-reported hostname cache (from miner's own API)
    if ip_or_name in API_HOSTNAMES:
        return API_HOSTNAMES[ip_or_name]
    if ip_only in API_HOSTNAMES:
        return API_HOSTNAMES[ip_only]

    # Check worker name from stratum config (e.g., "wallet.MyMiner" -> "MyMiner")
    if ip_or_name in API_WORKER_NAMES:
        return API_WORKER_NAMES[ip_or_name]
    if ip_only in API_WORKER_NAMES:
        return API_WORKER_NAMES[ip_only]

    # Try reverse DNS lookup (cached to avoid repeated slow lookups)
    if ip_only and ip_only[0].isdigit():
        rdns_name = _try_reverse_dns(ip_only)
        if rdns_name:
            return rdns_name

    return ip_or_name

CHECK_INTERVAL = CONFIG.get("check_interval", 120)

# ═══════════════════════════════════════════════════════════════════════════════
# P1 AUDIT FIX: Per-coin CHECK_INTERVAL for fast-block coins
# ═══════════════════════════════════════════════════════════════════════════════
# Fast-block coins (DGB 15-30s, DOGE 60s) need shorter check intervals to catch
# blocks quickly. The default 120s interval is too slow - a block could be found
# and orphaned before Sentinel even notices. Use coin-aware intervals.
COIN_CHECK_INTERVALS = {
    # Fast block coins - check more frequently
    "DGB": 30,          # DigiByte: 15-30s block time, check every 30s
    "DGB-SCRYPT": 30,   # DigiByte Scrypt algo
    "FBTC": 20,         # Fractal Bitcoin: 30s block time, check every 20s
    "DOGE": 45,         # Dogecoin: 60s block time, check every 45s
    "SYS": 45,          # Syscoin: ~60s block time, check every 45s
    "XMY": 45,          # Myriadcoin: ~60s per algo, check every 45s
    "LTC": 60,          # Litecoin: 2.5min block time, check every 60s
    # Standard block coins - use default interval
    "BTC": 120,         # Bitcoin: 10min block time
    "BCH": 120,         # Bitcoin Cash: 10min block time
    "BC2": 120,         # Block Creator 2
    "NMC": 120,         # Namecoin: ~10min block time
    "QBX": 60,          # Q-BitX: 150s block time, check every 60s
    # Other coins - use default
    "PEP": 60,          # Pepecoin
    "CAT": 60,          # Catcoin
}

def get_check_interval(coin=None):
    """Get the appropriate check interval for the given coin.

    P1 AUDIT FIX: Returns shorter intervals for fast-block coins.
    """
    if coin is None:
        coin = get_primary_coin()
    if coin and coin.upper() in COIN_CHECK_INTERVALS:
        return COIN_CHECK_INTERVALS[coin.upper()]
    return CHECK_INTERVAL  # Default fallback

# Master toggles for alerts and health monitoring
ALERTS_ENABLED = CONFIG.get("alerts_enabled", True)
HEALTH_MONITORING_ENABLED = CONFIG.get("health_monitoring_enabled", True)

REPORT_FREQUENCY = CONFIG.get("report_frequency", "6h")  # "6h", "daily", or "off"
REPORT_HOURS = CONFIG.get("report_hours", [6, 12, 18]) if REPORT_FREQUENCY == "6h" else []  # No midnight report by default
MAJOR_REPORT_HOUR = CONFIG.get("major_report_hour", 6)
REPORT_WINDOW = 30  # Extended from 10 to 30 minutes to prevent missed reports

# Individual report toggles
ENABLE_6H_REPORTS = CONFIG.get("enable_6h_reports", True)
ENABLE_WEEKLY_REPORTS = CONFIG.get("enable_weekly_reports", True)
ENABLE_MONTHLY_REPORTS = CONFIG.get("enable_monthly_reports", True)
ENABLE_QUARTERLY_REPORTS = CONFIG.get("enable_quarterly_reports", True)
WEEKLY_REPORT_DAY = CONFIG.get("weekly_report_day", 0)
MONTHLY_REPORT_DAY = CONFIG.get("monthly_report_day", 1)
QUIET_START = CONFIG.get("quiet_hours_start", 22)
QUIET_END = CONFIG.get("quiet_hours_end", 6)
QUIET_HOURS_ENABLED = True

# Final report before quiet hours (e.g., "21:45" for 9:45 PM)
# This allows a non-hourly report time just before quiet hours start
_final_report_raw = CONFIG.get("final_report_time", "21:55")
FINAL_REPORT_TIME = None  # Tuple of (hour, minute) or None if disabled
if _final_report_raw:
    try:
        _parts = _final_report_raw.split(":")
        FINAL_REPORT_TIME = (int(_parts[0]), int(_parts[1]))
    except (ValueError, IndexError):
        logger.warning(f"Invalid final_report_time format: {_final_report_raw}, expected HH:MM")
FINAL_REPORT_WINDOW = 15  # 15-minute window for final report (shorter since it's precise)

def _next_report_label():
    """Calculate human-readable time until next scheduled report."""
    if REPORT_FREQUENCY == "daily":
        return "24h"
    now = local_now()
    # Build sorted list of all scheduled report times as (hour, minute)
    times = sorted([(h, 0) for h in REPORT_HOURS] + ([FINAL_REPORT_TIME] if FINAL_REPORT_TIME else []))
    if not times:
        return "N/A"
    now_mins = now.hour * 60 + now.minute
    for h, m in times:
        t = h * 60 + m
        if t > now_mins:
            hrs, mins = divmod(t - now_mins, 60)
            return f"{hrs}h {mins}m" if hrs and mins else f"{hrs}h" if hrs else f"{mins}m"
    # Wrapped to tomorrow
    first_h, first_m = times[0]
    hrs, mins = divmod((24 * 60 - now_mins) + first_h * 60 + first_m, 60)
    return f"{hrs}h {mins}m" if hrs and mins else f"{hrs}h"

def _is_final_report_now():
    """Check if we're currently in the final report window (pre-quiet hours)."""
    if not FINAL_REPORT_TIME:
        return False
    now = local_now()
    fh, fm = FINAL_REPORT_TIME
    return now.hour == fh and fm <= now.minute < fm + FINAL_REPORT_WINDOW

ALERT_BYPASS_QUIET = {
    # CRITICAL - Always wake up for these (bypass quiet hours)
    "block_found": True,           # You found a block! Always celebrate
    "temp_critical": True,         # Hardware emergency
    "hashrate_crash": False,       # Suppress during quiet hours — visible in intel reports
    "coin_node_down": True,        # Node emergency
    "ha_vip_change": True,         # HA failover event
    "ha_state_change": True,       # HA cluster state change
    "ha_promoted": True,           # Node promoted to master - always alert
    "ha_demoted": True,            # Node demoted to backup - always alert
    "ha_replication_lag": True,    # Replication lag - failover readiness at risk
    "ha_resync": True,             # Post-failover resync - operational awareness

    # IMPORTANT - Bypass quiet hours (operational awareness)
    "miner_offline": True,         # Miner went down
    "miner_online": True,          # Miner came back
    "pool_hashrate_drop": True,    # Significant pool hashrate drop

    # SCHEDULED REPORTS - Bypass quiet hours (user explicitly scheduled these)
    # Reports only fire at REPORT_HOURS times [6, 12, 18] — no midnight report
    "6h_report": True,
    "weekly_report": True,
    "monthly_earnings": True,
    "startup_summary": True,
    "maintenance_reminder": True,  # Monthly maintenance reminder (1st of month at 8am)

    # IMPORTANT - Hardware/operational alerts (bypass quiet hours)
    "excessive_restarts": True,    # Hardware issue - frequent reboots need attention
    "chronic_issue": True,         # Recurring problem needs investigation

    # SCHEDULED REPORTS - Bypass quiet hours (user expects these)
    "quarterly_report": True,      # Scheduled quarterly report

    # INFORMATIONAL - Respect quiet hours (can wait until morning)
    "high_odds": False,            # Nice to know, not urgent
    "coin_config_change": False,   # Consolidated coin/mode config changes
    "coin_change": False,          # Config change, not urgent
    "coin_sync_behind": False,     # Sync issues can wait
    "temp_warning": False,         # Warning, not critical
    "zombie_miner": False,         # Stale miner, can wait
    "power_event": False,          # Power fluctuation, informational
    "auto_restart": False,         # Self-healing worked, informational
    "miner_reboot": False,         # Miner rebooted, informational
    "degradation": False,          # Hashrate degradation, can wait
    "hashrate_divergence": False,  # Pool/miner discrepancy, can wait
    "special_date": False,         # Celebration/milestone, not urgent
    "update_available": False,     # Software update notification, can wait
    "block_orphaned": True,        # Block was orphaned - always alert immediately (P0 audit fix)

    # THERMAL PROTECTION - Always wake up (hardware emergency)
    "thermal_shutdown": True,          # CRITICAL - ASIC frequency set to 0

    # NEW MONITORING ALERTS
    "fan_failure": False,              # Batched - can wait for morning
    "share_rejection_spike": False,    # Batched
    "orphan_rate_spike": True,         # Block loss - always alert
    "zmq_stale": False,                # Batched
    "worker_count_drop": False,        # Batched
    "share_loss_rate": False,          # Batched
    "block_notify_mode_change": False, # Batched
    "ha_replica_drop": True,           # HA emergency - always alert

    # INFRASTRUCTURE CRITICAL - Always wake up
    "circuit_breaker": True,           # Pool actively dropping shares
    "backpressure": True,              # Pool buffer overflow
    "wal_errors": True,                # Database write failures - silent data loss
    "zmq_disconnected": True,          # ZMQ socket down - instant detection

    # SECURITY - Always wake up
    "stratum_url_mismatch": True,      # Possible firmware hijack or misconfiguration
    "wallet_drop": True,               # Unexpected wallet balance decrease - possible theft

    # HARDWARE MONITORING
    "hashboard_dead": True,            # Discrete 33% capacity loss - bypass quiet hours
    "hw_error_rate": False,            # Predictive, can wait for morning

    # ECONOMIC / INFORMATIONAL
    "best_share": False,               # Celebratory, not urgent

    # FINANCIAL - Price and revenue alerts
    "price_crash": True,               # Sudden price drop - wake up (may need to shut down)
    "sats_surge": False,               # Informational - can wait for morning
    "payout_received": False,          # Celebratory - can wait for morning
    "missing_payout": True,            # Missing expected payout - possible issue
    "revenue_decline": False,          # Trend alert - can wait for morning
}

def get_alert_cooldowns():
    """Get alert cooldowns from config, with sensible defaults."""
    defaults = {
        # high_odds cooldown handled per-coin in monitoring loop (4h per coin)
        # Miner health alerts
        "temp_warning": 3600,          # 1 hour
        "temp_critical": 0,            # No cooldown - always alert critical temps
        "zombie_miner": 3600,          # 1 hour
        "degradation": 3600,           # 1 hour
        "miner_reboot": 600,           # 10 minutes
        "miner_offline": 0,            # No cooldown - always alert immediately
        "miner_online": 0,             # No cooldown - always alert immediately
        "auto_restart": 1800,          # 30 minutes - prevent spam from repeated attempts
        "excessive_restarts": 3600,    # 1 hour - prevent spam from flapping
        "chronic_issue": 3600,         # 1 hour - prevent spam
        "hashrate_divergence": 3600,   # 1 hour - pool/miner discrepancy
        # Fleet/network alerts
        "power_event": 600,            # 10 minutes
        "hashrate_crash": 21600,       # 6 hours - prevent repeated crash alerts on sustained drops
        "pool_hashrate_drop": 1800,    # 30 minutes
        # Block alerts
        "block_found": 0,              # No cooldown - always celebrate!
        "block_orphaned": 0,           # No cooldown - critical event (P0 audit fix)
        # Multi-coin mode alerts (no cooldown - always alert on config changes)
        "coin_config_change": 0,
        "coin_change": 0,
        # Per-coin health alerts (1 hour cooldown to prevent spam)
        "coin_node_down": 3600,
        "coin_sync_behind": 3600,
        # HA/VIP alerts (no cooldown for state changes)
        "ha_vip_change": 0,
        "ha_state_change": 0,
        "ha_promoted": 0,                  # No cooldown - critical role change
        "ha_demoted": 0,                   # No cooldown - critical role change
        "ha_replication_lag": 3600,        # 1 hour - prevent spam during sustained lag
        "ha_resync": 1800,                 # 30 minutes - resync updates
        # Report/notification alerts (no cooldown - scheduled delivery)
        "6h_report": 0,
        "weekly_report": 0,
        "monthly_earnings": 0,
        "quarterly_report": 0,
        "startup_summary": 0,
        "maintenance_reminder": 0,     # Monthly maintenance reminder (1st of month at 8am)
        "special_date": 0,             # No cooldown - yearly events
        "update_available": 86400,     # 24 hours - once per day max
        # Thermal protection and new monitoring alerts
        "thermal_shutdown": 0,             # No cooldown - always alert on thermal emergency
        "fan_failure": 1800,               # 30 minutes
        "share_rejection_spike": 3600,     # 1 hour
        "orphan_rate_spike": 3600,         # 1 hour
        "zmq_stale": 1800,                 # 30 minutes
        "worker_count_drop": 1800,         # 30 minutes
        "share_loss_rate": 1800,           # 30 minutes
        "block_notify_mode_change": 3600,  # 1 hour
        "ha_replica_drop": 3600,           # 1 hour
        # Infrastructure critical alerts
        "circuit_breaker": 0,              # No cooldown - pool is broken
        "backpressure": 300,               # 5 minutes - can fluctuate
        "wal_errors": 0,                   # No cooldown - data loss
        "zmq_disconnected": 1800,          # 30 minutes
        # Security alerts
        "stratum_url_mismatch": 0,         # No cooldown - always alert
        # Hardware monitoring
        "hashboard_dead": 3600,            # 1 hour
        "hw_error_rate": 3600,             # 1 hour
        # Economic / informational
        "best_share": 0,                   # No cooldown - milestone
        # Financial alerts
        "price_crash": 14400,              # 4 hours - prevent spam during volatile market
        "payout_received": 0,              # No cooldown - event-driven (balance change)
        "wallet_drop": 3600,               # 1 hour - prevent repeated alerts on fluctuating balance
        "missing_payout": 86400,           # 24 hours - daily check
        "revenue_decline": 86400,          # 24 hours - daily check
        # API Sentinel alerts (bridged from Go pool internals via /api/sentinel/alerts)
        # No additional cooldown — Go API Sentinel already applies its own 15min cooldown
        "pool_wal_stuck_entry": 0,
        "pool_block_drought": 0,
        "pool_share_db_critical": 0,
        "pool_share_db_degraded": 0,
        "pool_share_batch_dropped": 0,
        "pool_all_nodes_down": 0,
        "pool_chain_tip_stall": 0,
        "pool_daemon_no_peers": 0,
        "pool_daemon_low_peers": 0,
        "pool_wal_recovery_stuck": 0,
        "pool_miner_disconnect_spike": 0,
        "pool_hashrate_drop": 0,
        "pool_node_health_low": 0,
        "pool_wal_disk_space_low": 0,
        "pool_wal_file_count_high": 0,
        "pool_false_rejection_rate": 0,
        "pool_retry_storm": 0,
        "pool_payment_processor_stalled": 0,
        "pool_db_failover": 0,
        "pool_ha_flapping": 0,
        "pool_block_maturity_stall": 0,
        "pool_goroutine_limit": 0,
        "pool_goroutine_growth": 0,
    }
    # Merge with user config
    user_cooldowns = CONFIG.get("alert_cooldowns", {})
    defaults.update(user_cooldowns)
    return defaults

ALERT_COOLDOWNS = get_alert_cooldowns()

# Network hashrate thresholds per coin (in PH/s for SHA-256d, in GH/s for Scrypt)
# SHA-256d: DGB: ~50 PH/s, BTC: ~700 EH/s, BCH: ~5 EH/s, BC2: ~25-30 PH/s
# Scrypt: LTC: ~1 PH/s, DOGE: ~1.5 PH/s
COIN_THRESHOLDS = {
    # SHA-256d coins (thresholds in PH/s)
    "DGB": {"AMAZING": {"max": 30, "emoji": "🟢"}, "GREAT": {"max": 40, "emoji": "🔵"}, "GOOD": {"max": 50, "emoji": "🟡"}, "NORMAL": {"max": 95, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "BTC": {"AMAZING": {"max": 500000, "emoji": "🟢"}, "GREAT": {"max": 600000, "emoji": "🔵"}, "GOOD": {"max": 700000, "emoji": "🟡"}, "NORMAL": {"max": 900000, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "BCH": {"AMAZING": {"max": 3000, "emoji": "🟢"}, "GREAT": {"max": 4000, "emoji": "🔵"}, "GOOD": {"max": 5000, "emoji": "🟡"}, "NORMAL": {"max": 8000, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "BC2": {"AMAZING": {"max": 15, "emoji": "🟢"}, "GREAT": {"max": 25, "emoji": "🔵"}, "GOOD": {"max": 35, "emoji": "🟡"}, "NORMAL": {"max": 60, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "NMC": {"AMAZING": {"max": 300000, "emoji": "🟢"}, "GREAT": {"max": 400000, "emoji": "🔵"}, "GOOD": {"max": 500000, "emoji": "🟡"}, "NORMAL": {"max": 700000, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "SYS": {"AMAZING": {"max": 1, "emoji": "🟢"}, "GREAT": {"max": 5, "emoji": "🔵"}, "GOOD": {"max": 10, "emoji": "🟡"}, "NORMAL": {"max": 30, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "XMY": {"AMAZING": {"max": 0.5, "emoji": "🟢"}, "GREAT": {"max": 1, "emoji": "🔵"}, "GOOD": {"max": 3, "emoji": "🟡"}, "NORMAL": {"max": 10, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "FBTC": {"AMAZING": {"max": 100, "emoji": "🟢"}, "GREAT": {"max": 200, "emoji": "🔵"}, "GOOD": {"max": 400, "emoji": "🟡"}, "NORMAL": {"max": 800, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    # Scrypt coins (thresholds in PH/s - Scrypt network is much smaller)
    "LTC": {"AMAZING": {"max": 0.5, "emoji": "🟢"}, "GREAT": {"max": 0.8, "emoji": "🔵"}, "GOOD": {"max": 1.2, "emoji": "🟡"}, "NORMAL": {"max": 2.0, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "DOGE": {"AMAZING": {"max": 0.8, "emoji": "🟢"}, "GREAT": {"max": 1.2, "emoji": "🔵"}, "GOOD": {"max": 1.8, "emoji": "🟡"}, "NORMAL": {"max": 3.0, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "DGB-SCRYPT": {"AMAZING": {"max": 0.05, "emoji": "🟢"}, "GREAT": {"max": 0.08, "emoji": "🔵"}, "GOOD": {"max": 0.12, "emoji": "🟡"}, "NORMAL": {"max": 0.2, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "PEP": {"AMAZING": {"max": 0.001, "emoji": "🟢"}, "GREAT": {"max": 0.002, "emoji": "🔵"}, "GOOD": {"max": 0.005, "emoji": "🟡"}, "NORMAL": {"max": 0.01, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
    "CAT": {"AMAZING": {"max": 0.0005, "emoji": "🟢"}, "GREAT": {"max": 0.001, "emoji": "🔵"}, "GOOD": {"max": 0.002, "emoji": "🟡"}, "NORMAL": {"max": 0.005, "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "🔴"}},
}
THRESHOLDS = None  # Must be set via get_network_thresholds() with detected coin

def get_network_thresholds(coin=None):
    """Get network hashrate thresholds for a specific coin."""
    if coin is None:
        coin = get_primary_coin()
    if coin is None or coin.upper() not in COIN_THRESHOLDS:
        # Return neutral thresholds if coin not recognized
        return {"AMAZING": {"max": 0, "emoji": "⚪"}, "GREAT": {"max": 0, "emoji": "⚪"}, "GOOD": {"max": 0, "emoji": "⚪"}, "NORMAL": {"max": float('inf'), "emoji": "⚪"}, "HIGH": {"max": float('inf'), "emoji": "⚪"}}
    return COIN_THRESHOLDS.get(coin.upper())

# Network hashrate crash detection thresholds per coin (in PH/s)
# These values trigger "network crash" alerts when the network drops significantly
COIN_NET_CRASH = {
    # SHA-256d coins
    "DGB": {"floor": 45, "drop": 48, "reset": 52},      # DGB: ~50 PH/s network
    "BTC": {"floor": 600000, "drop": 650000, "reset": 700000},  # BTC: ~700 EH/s network
    "BCH": {"floor": 4000, "drop": 4500, "reset": 5000},  # BCH: ~5 EH/s network
    "BC2": {"floor": 20, "drop": 23, "reset": 28},      # BC2: ~25-30 PH/s network
    "NMC": {"floor": 300000, "drop": 350000, "reset": 450000},  # NMC: merge-mined with BTC
    "SYS": {"floor": 1, "drop": 3, "reset": 5},         # SYS: small network
    "XMY": {"floor": 0.3, "drop": 0.5, "reset": 1},     # XMY: small multi-algo network
    "FBTC": {"floor": 80, "drop": 100, "reset": 150},   # FBTC: newer chain
    "QBX": {"floor": 0.05, "drop": 0.08, "reset": 0.12},  # QBX: new small network
    # Scrypt coins (much smaller networks, values in PH/s)
    "LTC": {"floor": 0.8, "drop": 0.9, "reset": 1.1},   # LTC: ~1 PH/s network
    "DOGE": {"floor": 1.2, "drop": 1.4, "reset": 1.6},  # DOGE: ~1.5 PH/s network (merge-mined with LTC)
    "DGB-SCRYPT": {"floor": 0.04, "drop": 0.05, "reset": 0.07},  # DGB Scrypt algo
    "PEP": {"floor": 0.0005, "drop": 0.001, "reset": 0.002},  # PEP: small network
    "CAT": {"floor": 0.0002, "drop": 0.0005, "reset": 0.001},  # CAT: very small network
}

def get_net_crash_thresholds(coin=None):
    """Get network crash detection thresholds for a specific coin."""
    if coin is None:
        coin = get_primary_coin()
    if coin is None or coin.upper() not in COIN_NET_CRASH:
        # Return very low thresholds if coin not recognized (disables crash alerts)
        return {"floor": 0, "drop": 0, "reset": 0}
    return COIN_NET_CRASH.get(coin.upper())

NET_CRASH_PCT = 25  # Alert if network drops 25%+
NET_CRASH_FLOOR = None  # Must use get_net_crash_thresholds() for coin-specific values
NET_CRASH_SUSTAIN = 7200  # Must be sustained for 2 hours (7200 seconds)

# Pool hashrate drop thresholds
POOL_DROP_PCT = 50  # Alert if pool drops 50%+
POOL_DROP_SUSTAIN = 900  # Must be sustained for 15 minutes (900 seconds)

MINER_OFFLINE_TH = CONFIG.get("miner_offline_threshold_min", 10)
TEMP_WARN = CONFIG.get("temp_warning", 75)
TEMP_CRIT = CONFIG.get("temp_critical", 85)
RESTART_DROP_TH = 0.3
HEALTH_WARN_THRESHOLD = CONFIG.get("health_warn_threshold", 70)
HEALTH_WEIGHTS = {"uptime": 0.3, "temp_stability": 0.2, "hashrate_consistency": 0.3, "stale_rate": 0.2}
BLIP_ENABLED = CONFIG.get("blip_detection_enabled", True)
AUTO_RESTART = CONFIG.get("auto_restart_enabled", True)
AUTO_RESTART_MIN = CONFIG.get("auto_restart_min_offline", 20)
AUTO_RESTART_COOL = CONFIG.get("auto_restart_cooldown", 1800)
SATS_CHANGE_ALERT_PCT = CONFIG.get("sats_change_alert_pct", 15)
ODDS_TH = CONFIG.get("odds_alert_threshold", 30)

# Thermal protection settings (AxeOS emergency stop)
TEMP_EMERGENCY = CONFIG.get("temp_emergency", 95)           # Immediate stop temperature
THERMAL_SHUTDOWN_SUSTAINED_SEC = CONFIG.get("thermal_shutdown_sustained_sec", 90)  # Seconds at TEMP_CRIT before stop
THERMAL_SHUTDOWN_ENABLED = CONFIG.get("thermal_shutdown_enabled", True)

# Financial alert settings
PRICE_CRASH_PCT = CONFIG.get("price_crash_pct", 15)             # Alert when price drops 15%+ in 1 hour
PRICE_CRASH_ENABLED = CONFIG.get("price_crash_enabled", True)
PAYOUT_CHECK_INTERVAL = CONFIG.get("payout_check_interval", 3600)  # Check wallet balance every 1 hour
MISSING_PAYOUT_DAYS = CONFIG.get("missing_payout_days", 7)        # Alert if no payout for N days
REVENUE_DECLINE_PCT = CONFIG.get("revenue_decline_pct", 50)       # Alert when pace is 50%+ below last month

# ZMQ staleness thresholds per coin (seconds) - scaled to block time
# Threshold ~= 5-10x expected block time so we expect several blocks before alerting
COIN_ZMQ_STALE_THRESHOLDS = {
    # Thresholds are set to ~7x block time → <0.1% false positive rate (e^-7 ≈ 0.09%)
    # Block arrival is Poisson-distributed, so natural gaps exceed the average regularly.
    # At 7x block time, a natural gap this long happens once per ~1000 intervals (~rare).
    #
    # Fast block coins (15-30s block times)
    "DGB": 105,             # DigiByte: 15s blocks × 7 = 105s (~1.75 min)
    "DGB-SCRYPT": 105,      # DigiByte Scrypt: 15s blocks × 7
    # Medium-fast block coins (30-60s block times)
    "DOGE": 420,            # Dogecoin: 60s blocks × 7 = 420s (7 min)
    "PEP": 420,             # Pepecoin: ~60s blocks × 7
    "CAT": 420,             # Catcoin: ~60s blocks × 7
    # Medium block coins (150s block time)
    "LTC": 1050,            # Litecoin: 150s blocks × 7 = 1050s (~17.5 min)
    "QBX": 1050,            # Q-BitX: 150s blocks × 7 = 1050s (~17.5 min)
    # Slow block coins (600s block times)
    "BTC": 3600,            # Bitcoin: 600s blocks × 6 = 3600s (60 min, ~0.25% false positive)
    "BCH": 3600,            # Bitcoin Cash: 600s blocks × 6
    "BC2": 3600,            # Block Creator 2: 600s blocks × 6
    "NMC": 3600,            # Namecoin: 600s blocks × 6
    "SYS": 420,             # Syscoin: 60s blocks × 7
    "XMY": 420,             # Myriad: 60s blocks × 7
    "FBTC": 210,            # Fractal Bitcoin: 30s blocks × 7
}

def get_zmq_stale_threshold(coin=None):
    """Get ZMQ staleness threshold for the given coin, scaled to block time."""
    if coin is None:
        coin = get_primary_coin()
    if coin and coin.upper() in COIN_ZMQ_STALE_THRESHOLDS:
        return COIN_ZMQ_STALE_THRESHOLDS[coin.upper()]
    return CONFIG.get("zmq_stale_threshold", 300)  # Fallback default

# Update checking settings
UPDATE_CHECK_ENABLED = CONFIG.get("update_check_enabled", True)
UPDATE_CHECK_INTERVAL = CONFIG.get("update_check_interval", 21600)  # 6 hours in seconds
AUTO_UPDATE_MODE = CONFIG.get("auto_update_mode", "notify")  # "auto", "notify", or "disabled"

# Alert batching/aggregation settings
ALERT_BATCHING_ENABLED = CONFIG.get("alert_batching_enabled", True)
ALERT_BATCH_WINDOW = CONFIG.get("alert_batch_window_seconds", 300)  # 5 minutes

# Alert types that should NEVER be batched (always sent individually)
# Note: This controls BATCHING only. Quiet hours are controlled by ALERT_BYPASS_QUIET.
IMMEDIATE_ALERT_TYPES = {
    # Critical alerts - hardware/system emergencies
    "block_found",        # Always celebrate immediately!
    "block_orphaned",     # Block orphaned - critical loss event (P0 audit fix)
    "temp_critical",      # Hardware thermal emergency
    "coin_node_down",     # Node emergency
    "excessive_restarts", # Hardware issue needs attention
    "chronic_issue",      # Recurring problem needs investigation
    # Important operational alerts
    "miner_offline",      # Miner went down - needs attention
    "miner_online",       # Miner recovery notification
    "pool_hashrate_drop", # Significant pool hashrate drop
    "hashrate_crash",     # Network hashrate crash
    "high_odds",          # Mining opportunity
    "sats_surge",         # Coin/BTC sat value surge - swap opportunity
    "power_event",        # Fleet-wide power events (already aggregated)
    # HA cluster alerts
    "ha_vip_change",      # HA VIP failover events
    "ha_state_change",    # HA cluster state changes
    # Thermal protection and critical new alerts
    "thermal_shutdown",   # Hardware thermal emergency - ASIC stopped
    "orphan_rate_spike",  # Block loss event - always immediate
    "ha_replica_drop",    # HA replica loss - always immediate
    "ha_promoted",        # Node promoted to master - always immediate
    "ha_demoted",         # Node demoted to backup - always immediate
    "ha_replication_lag", # Replication lag - failover readiness at risk
    "ha_resync",          # Post-failover resync - operational awareness
    # Infrastructure critical - never batch
    "circuit_breaker",    # Pool dropping shares RIGHT NOW
    "backpressure",       # Pool buffer overflow
    "wal_errors",         # Database write failures
    "zmq_disconnected",   # ZMQ socket health failure
    # Security - never batch
    "stratum_url_mismatch", # Possible firmware hijack
    "wallet_drop",          # Unexpected balance decrease
    # Hardware - never batch
    "hashboard_dead",     # Discrete major capacity loss
    # Financial - never batch
    "price_crash",        # Sudden price drop needs immediate awareness
    "payout_received",    # Celebratory - immediate (respects quiet hours)
    "missing_payout",     # Possible issue - immediate
    # Configuration changes
    "coin_config_change", # Configuration changes
    "coin_change",        # Coin switch notification
    # Scheduled reports - always immediate
    "startup_summary",    # System startup notification
    "6h_report",          # Scheduled reports
    "weekly_report",
    "monthly_earnings",
    "quarterly_report",
    "special_date",       # Special date celebrations
    "update_available",   # Software update notification
}

# ═══════════════════════════════════════════════════════════════════════════════
# DISCORD EMBED COLORS - Standardized color scheme for consistency
# ═══════════════════════════════════════════════════════════════════════════════
# 🟢 GREEN  = Good news (block found, miner online, high odds, startup)
# 🔵 BLUE   = Informational (reports, summaries, periodic updates)
# 🟡 YELLOW = Warning (thermal warning, reboot, degradation)
# 🔴 RED    = Critical (offline, zombie, power event, critical temp)
# 🔵 CYAN   = Opportunity (network crash, network drop, hunting window)
# 🟠 ORANGE = Moderate concern (pool hashrate drop, restart attempt)
# 🟣 PURPLE = Celebration! (block found - the big one!)
# 🟡 GOLD   = Financial (monthly earnings, wallet updates)
# ═══════════════════════════════════════════════════════════════════════════════
COLORS = {
    "green": 0x00FF41,   # Success, positive events
    "blue": 0x00D9FF,    # Informational, reports
    "yellow": 0xFFFF00,  # Warnings, needs attention
    "red": 0xFF006E,     # Critical, immediate action
    "cyan": 0x00FFFF,    # Opportunity, network events
    "orange": 0xFF6B35,  # Moderate concern
    "gold": 0xFFD700,    # Financial, earnings
    "purple": 0xFF00FF,  # Block found celebration!
}

# Cyberpunk sentinel quotes
def get_quote(): return random.choice(get_themed_quotes())

def calc_efficiency(power_watts, hashrate_ghs):
    if not power_watts or not hashrate_ghs or hashrate_ghs <= 0: return None
    return power_watts / (hashrate_ghs / 1000)

# === MINER POLLING ===
def fetch_nmaxe(ip, timeout=5):
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        return None
    try:
        req = urllib.request.Request(f"http://{ip}/api/system/info", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            d = json.loads(resp.read().decode())
            # NMAxe firmware (v2.9.x+) uses nested stratum object and different field names.
            # pool URL: stratum.used.url  |  hostname: hostName  |  user: stratum.used.user
            stratum = d.get("stratum", {})
            used = stratum.get("used", {})
            pool_url = used.get("url", d.get("stratumURLUSED", ""))  # fallback for older firmware
            stratum_user = used.get("user", d.get("stratumUser", ""))
            worker_name = stratum_user.split(".")[-1] if "." in stratum_user else stratum_user
            fans_list = d.get("fans", [])
            fan_rpm = fans_list[0].get("rpm", 0) if fans_list else 0
            return {
                "hashrate_ghs": d.get("hashRate", 0), "power_watts": d.get("power", 0),
                "temps": {
                    "chip": d.get("asicTemp", d.get("temp")),        # asicTemp in v2.9.x, temp in older
                    "board": d.get("mcuTemp", d.get("boardTemp")),   # mcuTemp in v2.9.x
                    "vr": d.get("vcoreTemp", d.get("vrTemp")),       # vcoreTemp in v2.9.x
                },
                "uptime": d.get("uptimeSeconds", d.get("uptime")),
                "accepted": d.get("sharesAccepted", 0), "rejected": d.get("sharesRejected", 0), "stale": 0,
                "pool_url": pool_url, "hostname": d.get("hostName", d.get("hostname")),
                "stratum_user": stratum_user, "worker_name": worker_name,
                "fan_speed": fan_rpm,
                "frequency": d.get("freqReq", d.get("frequency", 0)),
                "voltage": d.get("vcoreActual", d.get("vcoreReq", d.get("coreVoltage", 0))),
                "best_diff": d.get("bestDiffEver", d.get("bestDiff", "0")),
                "version": d.get("fwVersion", d.get("version", "Unknown")),
            }
    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, json.JSONDecodeError, OSError):
        return None

def fetch_nerdqaxe(ip, timeout=5):
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        return None
    try:
        req = urllib.request.Request(f"http://{ip}/api/system/info", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode(errors='ignore')
            d = json.loads(raw[:raw.rfind('}')+1])
            # Extract worker name from stratumUser (format: wallet.worker)
            stratum_user = d.get("stratumUser", "")
            worker_name = stratum_user.split(".")[-1] if "." in stratum_user else stratum_user
            return {
                "hashrate_ghs": d.get("hashRate", 0), "power_watts": d.get("power", 0),
                "temps": {"chip": d.get("temp"), "vr": d.get("vrTemp")},
                "uptime": d.get("uptimeSeconds"),
                "accepted": d.get("sharesAccepted", 0), "rejected": d.get("sharesRejected", 0),
                "found_blocks": d.get("totalFoundBlocks", 0),
                "pool_url": f"{d.get('stratumURL', '')}:{d.get('stratumPort', '')}",
                "hostname": d.get("hostname"),
                "stratum_user": stratum_user, "worker_name": worker_name,
            }
    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, json.JSONDecodeError, OSError, ValueError):
        return None

def fetch_goldshell(ip, port=80, timeout=5):
    """
    Fetch data from Goldshell miners via HTTP API.
    Supports: Mini DOGE, LT5, LT5 Pro, LT6, and other Goldshell Scrypt miners.
    API endpoints:
      - /mcb/status - returns model, hardware info
      - /mcb/cgminer?cgminercmd=summary - returns CGMiner-like summary data
    Reference: https://github.com/jorgedlcruz/goldshell-miner-grafana
    """
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        return None
    try:
        # Get summary data from CGMiner-style API
        req = urllib.request.Request(
            f"http://{ip}/mcb/cgminer?cgminercmd=summary",
            headers={"User-Agent": f"SpiralSentinel/{__version__}"}
        )
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            d = json.loads(resp.read().decode())

        result = {
            "hashrate_ghs": None, "found_blocks": 0, "temps": {},
            "uptime": None, "accepted": 0, "rejected": 0, "stale": 0,
            "power_watts": 0, "fans": []
        }

        # Parse summary data - Goldshell wraps in 'data' key
        if "data" in d and isinstance(d["data"], dict):
            s = d["data"]
            # Hashrate - try GHS first, then MHS
            ghs = s.get("GHS av", 0) or s.get("GHS 5s", 0)
            if not ghs:
                mhs = s.get("MHS av", 0) or s.get("MHS 5s", 0)
                ghs = float(mhs) / 1000 if mhs else 0
            if ghs > 0:
                result["hashrate_ghs"] = float(ghs)
            result.update({
                "found_blocks": s.get("Found Blocks", 0),
                "uptime": s.get("Elapsed"),
                "accepted": s.get("Accepted", 0),
                "rejected": s.get("Rejected", 0),
                "stale": s.get("Stale", 0)
            })

        # Get status info for model/temps
        try:
            status_req = urllib.request.Request(
                f"http://{ip}/mcb/status",
                headers={"User-Agent": f"SpiralSentinel/{__version__}"}
            )
            with urllib.request.urlopen(status_req, timeout=timeout) as status_resp:
                status = json.loads(status_resp.read().decode())
                if "model" in status:
                    result["device_model"] = f"Goldshell {status['model']}"
                # Temperature fields vary by model
                if "temperature" in status:
                    result["temps"]["chip"] = status["temperature"]
                if "env_temp" in status:
                    result["temps"]["board"] = status["env_temp"]
        except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, json.JSONDecodeError, OSError):
            pass  # Status endpoint is optional

        return result if result["hashrate_ghs"] is not None else None

    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, json.JSONDecodeError, OSError):
        return None


def fetch_hammer(ip, timeout=5):
    """
    Fetch data from PlebSource Hammer Miner via AxeOS-style HTTP API.
    Hammer Miner: 105 MH/s Scrypt, 25W, WiFi connected.
    Uses same API structure as BitAxe/NMaxe devices.
    """
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        return None
    try:
        req = urllib.request.Request(f"http://{ip}/api/system/info", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            d = json.loads(resp.read().decode())
            # Hammer Miner reports hashrate in MH/s for Scrypt
            # Convert to GH/s for consistency
            hashrate_mhs = d.get("hashRate", 0)
            hashrate_ghs = hashrate_mhs / 1000 if hashrate_mhs else 0
            # Extract worker name from stratumUser (format: wallet.worker)
            stratum_user = d.get("stratumUser", "")
            worker_name = stratum_user.split(".")[-1] if "." in stratum_user else stratum_user
            return {
                "hashrate_ghs": hashrate_ghs, "power_watts": d.get("power", 0),
                "temps": {k2: d[k1] for k1, k2 in [("temp", "chip"), ("boardTemp", "board"), ("vrTemp", "vr")] if k1 in d},
                "uptime": d.get("uptimeSeconds", d.get("uptime")),
                "accepted": d.get("sharesAccepted", 0), "rejected": d.get("sharesRejected", 0), "stale": 0,
                "pool_url": d.get("stratumURLUSED", ""), "hostname": d.get("hostname"),
                "stratum_user": stratum_user, "worker_name": worker_name,
            }
    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, json.JSONDecodeError, OSError):
        return None


def fetch_esp32miner(ip, timeout=5):
    """
    DEPRECATED: ESP32 Miner V2 has NO HTTP API - use fetch_esp32miner_from_pool() instead.

    This function attempts to fetch via HTTP API which does NOT exist on ESP32 Miner V2.
    It's kept for potential future ESP32 Miner variants that might expose an API,
    or for NerdQAxe devices that run modified AxeOS firmware.

    For standard ESP32 Miner V2 (ESP32-only), use fetch_esp32miner_from_pool() which
    polls the pool's stratum connections API instead.
    """
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        return None

    # Try ESP32 Miner-specific endpoints first, then fall back to AxeOS-style
    endpoints = ["/api/status", "/status", "/api/system/info"]

    for endpoint in endpoints:
        try:
            req = urllib.request.Request(
                f"http://{ip}{endpoint}",
                headers={"User-Agent": f"SpiralSentinel/{__version__}"}
            )
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                d = json.loads(resp.read().decode())

                # ESP32 Miner reports hashrate in H/s (very low - kH/s range)
                # Check for ESP32 Miner-specific fields first
                hashrate_hs = d.get("hashRate", d.get("hashrate", 0))

                # ESP32 Miner hashrate is in H/s, convert to GH/s
                # If hashrate seems like it's already in GH/s (from AxeOS API), don't convert
                if hashrate_hs > 1000000:  # Likely already in higher unit
                    hashrate_ghs = hashrate_hs / 1e9  # Convert H/s to GH/s
                elif hashrate_hs > 1000:  # Likely in kH/s
                    hashrate_ghs = hashrate_hs / 1e6
                else:  # Could be in GH/s already or very low
                    hashrate_ghs = hashrate_hs / 1e9 if hashrate_hs > 100 else hashrate_hs

                # Extract worker name from stratumUser if available
                stratum_user = d.get("stratumUser", d.get("stratum_user", ""))
                worker_name = stratum_user.split(".")[-1] if "." in stratum_user else stratum_user

                # ESP32 Miner uses 'valid'/'invalid' for shares, AxeOS uses 'sharesAccepted'/'sharesRejected'
                accepted = d.get("valid", d.get("sharesAccepted", d.get("valids", 0)))
                rejected = d.get("invalid", d.get("sharesRejected", d.get("invalids", 0)))

                # Best difficulty found (ESP32 Miner-specific, useful for lottery mining)
                best_diff = d.get("bestDiff", d.get("best_diff", d.get("bestDifficulty", "")))

                return {
                    "hashrate_ghs": hashrate_ghs,
                    "power_watts": d.get("power", 2),  # Default 2W for ESP32
                    "temps": {k2: d[k1] for k1, k2 in [("temp", "chip"), ("boardTemp", "board")] if k1 in d},
                    "uptime": d.get("uptimeSeconds", d.get("uptime", d.get("elapsed", 0))),
                    "accepted": accepted,
                    "rejected": rejected,
                    "stale": 0,
                    "pool_url": d.get("stratumURL", d.get("pool", "")),
                    "hostname": d.get("hostname", d.get("name", "ESP32 Miner")),
                    "stratum_user": stratum_user,
                    "worker_name": worker_name,
                    "best_diff": best_diff,  # ESP32 Miner-specific: best difficulty found
                }
        except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, json.JSONDecodeError, OSError):
            continue

    return None


def restart_esp32miner(ip, timeout=10):
    """Restart ESP32 Miner V2 device."""
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        return False
    for ep in ["/api/restart", "/restart", "/reboot", "/api/system/restart"]:
        try:
            req = urllib.request.Request(f"http://{ip}{ep}", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                if resp.status in [200, 201, 202, 204]:
                    return True
        except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError):
            continue
    return False


def restart_nerdqaxe(ip, timeout=10):
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        return False
    for ep in ["/api/system/restart", "/restart", "/reboot"]:
        try:
            req = urllib.request.Request(f"http://{ip}{ep}", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                if resp.status in [200, 201, 202, 204]: return True
        except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError):
            continue
    return False

def restart_nmaxe(ip, timeout=10):
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        logger.error(f"restart_nmaxe() blocked - invalid IP: {ip}")
        return False
    logger.info(f"restart_nmaxe() called for {ip}")
    for ep, m in [("/api/system/restart", "POST"), ("/api/system/reboot", "POST")]:
        try:
            req = urllib.request.Request(f"http://{ip}{ep}", data=b'{}', headers={"User-Agent": f"SpiralSentinel/{__version__}", "Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                if resp.status in [200, 201, 202, 204]: return True
        except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError):
            continue
    return False

def restart_axeos(ip, timeout=10):
    """Restart any AxeOS-based device (NerdAxe, NMaxe, BitAxe, Avalon Nano 3S with AxeOS, etc.)"""
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        logger.error(f"restart_axeos() blocked - invalid IP: {ip}")
        return False
    logger.info(f"restart_axeos() called for {ip}")
    # Try multiple endpoints - different AxeOS versions use different paths
    for ep, method in [("/api/system/restart", "POST"), ("/api/system/reboot", "POST"),
                       ("/restart", "GET"), ("/reboot", "GET")]:
        try:
            if method == "POST":
                req = urllib.request.Request(f"http://{ip}{ep}", data=b'{}',
                    headers={"User-Agent": f"SpiralSentinel/{__version__}", "Content-Type": "application/json"})
            else:
                req = urllib.request.Request(f"http://{ip}{ep}",
                    headers={"User-Agent": f"SpiralSentinel/{__version__}"})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                if resp.status in [200, 201, 202, 204]:
                    logger.info(f"restart_axeos() success for {ip} via {ep}")
                    return True
        except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError):
            continue
    logger.warning(f"restart_axeos() failed for {ip} - no endpoint responded")
    return False


def emergency_stop_axeos(ip, timeout=10):
    """Emergency stop an AxeOS device by setting frequency to 0.

    This is a thermal protection measure - the miner will stop hashing entirely.
    The user MUST manually restore the frequency via the AxeOS web UI or API.

    Returns True if the frequency was successfully set to 0, False otherwise.
    """
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        logger.error(f"emergency_stop_axeos() blocked - invalid IP: {ip}")
        return False
    logger.warning(f"THERMAL EMERGENCY: emergency_stop_axeos() setting frequency=0 for {ip}")
    try:
        payload = json.dumps({"frequency": 0}).encode("utf-8")
        req = urllib.request.Request(
            f"http://{ip}/api/system",
            data=payload,
            method="PATCH",
            headers={
                "User-Agent": f"SpiralSentinel/{__version__}",
                "Content-Type": "application/json"
            }
        )
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            if resp.status in [200, 201, 202, 204]:
                logger.warning(f"THERMAL EMERGENCY: Successfully stopped {ip} (frequency=0)")
                return True
    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError) as e:
        logger.error(f"THERMAL EMERGENCY: Failed to stop {ip}: {e}")
    return False


def restart_miner(miner_type, ip, port=4028):
    """Universal miner restart function. Tries appropriate method based on miner type.

    For AxeOS-based miners (nmaxe, nerdqaxe, axeos, bitaxe, hammer): Uses HTTP API
    For CGMiner-based miners (avalon, antminer, whatsminer, innosilicon): Tries HTTP first, limited support

    Returns True if restart command was sent successfully, False otherwise.
    """
    if not validate_miner_ip(ip):
        logger.error(f"restart_miner() blocked - invalid IP: {ip}")
        return False

    logger.info(f"restart_miner() called for {miner_type} at {ip}")

    # ═══════════════════════════════════════════════════════════════════════════════
    # AxeOS/ESP-Miner devices - use HTTP API /api/system/restart
    # Includes: BitAxe, NMAxe, NerdAxe, NerdQAxe, NerdOctaxe, QAxe, Hammer, etc.
    # ═══════════════════════════════════════════════════════════════════════════════
    axeos_types = [
        "axeos", "bitaxe", "nmaxe", "nerdaxe", "nerdqaxe", "nerdoctaxe",
        "qaxe", "qaxeplus", "hammer", "esp32miner",
        "luckyminer", "jingleminer", "zyber"
    ]
    if miner_type in axeos_types:
        return restart_axeos(ip)

    # Avalon Nano with AxeOS firmware - try HTTP first, fallback to CGMiner
    if miner_type == "avalon":
        # Avalon Nano 3S often runs AxeOS, try HTTP restart
        if restart_axeos(ip):
            return True
        # CGMiner-based Avalons don't have restart API
        logger.warning(f"Avalon at {ip} doesn't support HTTP restart (CGMiner-only firmware)")
        return False

    # ═══════════════════════════════════════════════════════════════════════════════
    # Goldshell - has web interface for restart
    # ═══════════════════════════════════════════════════════════════════════════════
    if miner_type == "goldshell":
        for ep in ["/mcb/restart", "/mcb/reboot", "/restart", "/reboot"]:
            try:
                req = urllib.request.Request(f"http://{ip}{ep}",
                    headers={"User-Agent": f"SpiralSentinel/{__version__}"})
                with urllib.request.urlopen(req, timeout=10) as resp:
                    if resp.status in [200, 201, 202, 204]:
                        logger.info(f"restart_miner() success for goldshell at {ip} via {ep}")
                        return True
            except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError):
                continue
        return False

    # ═══════════════════════════════════════════════════════════════════════════════
    # Industrial ASICs - limited restart support via web interface
    # Includes: Antminer, Whatsminer, Innosilicon, FutureBit, Canaan, Ebang
    # ═══════════════════════════════════════════════════════════════════════════════
    industrial_types = [
        "antminer", "antminer_scrypt", "whatsminer", "innosilicon",
        "futurebit", "canaan", "ebang"
    ]
    if miner_type in industrial_types:
        # Try common web interface endpoints
        for ep in ["/cgi-bin/reboot.cgi", "/api/reboot", "/reboot"]:
            try:
                req = urllib.request.Request(f"http://{ip}{ep}",
                    headers={"User-Agent": f"SpiralSentinel/{__version__}"})
                with urllib.request.urlopen(req, timeout=10) as resp:
                    if resp.status in [200, 201, 202, 204]:
                        logger.info(f"restart_miner() success for {miner_type} at {ip} via {ep}")
                        return True
            except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError):
                continue
        logger.warning(f"{miner_type} at {ip} doesn't support unauthenticated restart")
        return False

    logger.warning(f"restart_miner() - unknown miner type: {miner_type}")
    return False


def _cgminer(ip, port, cmd, timeout=5):
    # SECURITY: Validate IP to prevent SSRF
    if not validate_miner_ip(ip):
        return None
    # SECURITY: Validate port range
    if not isinstance(port, int) or port < 1 or port > 65535:
        return None
    # SECURITY: Validate command against whitelist
    allowed_commands = {"summary", "stats", "devs", "pools", "version", "config"}
    if cmd not in allowed_commands:
        logger.error(f"SECURITY: Blocked unknown CGMiner command: {cmd}")
        return None
    # SECURITY: Max response size to prevent memory exhaustion (1MB)
    MAX_RESPONSE_SIZE = 1024 * 1024
    sock = None
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(timeout)
        sock.connect((ip, port))
        sock.send(f'{{"command":"{cmd}"}}'.encode())
        resp = b""
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            resp += chunk
            # SECURITY: Prevent memory exhaustion from malicious/buggy devices
            if len(resp) > MAX_RESPONSE_SIZE:
                logger.warning(f"CGMiner response from {ip} exceeded max size, truncating")
                break
        text = resp.decode(errors='ignore').rstrip('\x00')
        return json.loads(text[text.index('|{')+1:] if '|{' in text else text)
    except (socket.timeout, socket.error, ConnectionRefusedError, OSError):
        return None
    except (json.JSONDecodeError, ValueError):
        return None
    finally:
        if sock:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                sock.close()
            except OSError:
                pass

def _cgminer_get_worker(ip, port, timeout=5):
    """Extract worker name from CGMiner pools command for pool share validation."""
    pools = _cgminer(ip, port, "pools", timeout)
    if pools and "POOLS" in pools:
        for p in pools["POOLS"]:
            if p.get("Status") == "Alive" or p.get("Stratum Active"):
                stratum_user = p.get("User", "")
                worker_name = stratum_user.split(".")[-1] if "." in stratum_user else stratum_user
                return stratum_user, worker_name
    return "", ""

def fetch_avalon(ip, port=4028, timeout=5):
    """
    Fetch data from Avalon miners via CGMiner API.
    Supports: Avalon Nano, Nano 3, Nano 3s, and other Canaan Avalon devices.

    Note: Avalon Nano embeds temperature and power data in the 'MM ID0' string field,
    requiring regex parsing to extract TMax, TAvg, per-chip temps, and wattage.
    """
    r = {"hashrate_ghs": None, "found_blocks": 0, "temps": {}, "uptime": None, "accepted": 0, "rejected": 0, "stale": 0, "stratum_user": "", "worker_name": "", "power_watts": None, "device_model": ""}
    d = _cgminer(ip, port, "summary", timeout)
    if d and "SUMMARY" in d:
        for s in d["SUMMARY"]:
            mhs = s.get("MHS av", 0) or s.get("MHS 5s", 0)
            if mhs > 0: r["hashrate_ghs"] = mhs / 1000
            r.update({"found_blocks": s.get("Found Blocks", 0), "uptime": s.get("Elapsed"), "accepted": s.get("Accepted", 0), "rejected": s.get("Rejected", 0), "stale": s.get("Stale", 0)})
    st = _cgminer(ip, port, "stats", timeout)
    if st and "STATS" in st:
        for s in st["STATS"]:
            # Extract device model from CGMiner stats response
            # ID field varies by model: "AVALON0" (Nano series), "AVA100" (A10), "AVA1x0" (A11-A15)
            # All Avalon/Canaan devices use "AVA" prefix. Safe — fetch_avalon() only called for avalon config.
            # MM ID0 field: "Ver[Nano3s-...]" or "Ver[1066-...]" contains specific model
            stat_id = s.get("ID", "")
            mm_id = s.get("MM ID0", "")
            if not r["device_model"] and stat_id.upper().startswith("AVA"):
                # Extract specific model from Ver[...] in MM ID0
                ver_match = re.search(r'Ver\[([A-Za-z0-9]+)', mm_id) if mm_id else None
                if ver_match:
                    r["device_model"] = f"Avalon {ver_match.group(1)}"  # e.g., "Avalon Nano3s"
                else:
                    r["device_model"] = f"Avalon {stat_id}"  # Fallback: "Avalon AVALON0"
            # Standard CGMiner temp fields (older Avalon models)
            for k, t in [("temp", "chip"), ("temp2", "board")]:
                if k in s: r["temps"][t] = s[k]
            # Avalon Nano: Parse temperature from MM ID0 string
            # Format: "...TMax[94] TAvg[90] OTemp[68]...PVT_T0[ 85  86  92...]..."
            if mm_id and not r["temps"]:
                # Extract TAvg (average chip temperature) - most useful for monitoring
                tavg_match = re.search(r'TAvg\[(\d+)\]', mm_id)
                if tavg_match:
                    r["temps"]["chip"] = int(tavg_match.group(1))
                # Extract TMax (max chip temperature) as secondary
                tmax_match = re.search(r'TMax\[(\d+)\]', mm_id)
                if tmax_match:
                    r["temps"]["max"] = int(tmax_match.group(1))
                # Extract OTemp (outside/ambient temperature) if available
                otemp_match = re.search(r'OTemp\[(\d+)\]', mm_id)
                if otemp_match:
                    otemp = int(otemp_match.group(1))
                    if otemp > 0:  # Filter out invalid readings
                        r["temps"]["board"] = otemp
                # Extract power consumption from MM ID0
                # Avalon Nano 3/3s uses MPO[watts] for Mining Power Output
                mpo_match = re.search(r'MPO\[(\d+)\]', mm_id)
                if mpo_match:
                    r["power_watts"] = int(mpo_match.group(1))
                # Fallback: PS field last value is often power in watts
                # Format: PS[status vin vout pin pout freq watts]
                if not r["power_watts"]:
                    ps_match = re.search(r'PS\[[\d\s]+\s(\d+)\]', mm_id)
                    if ps_match:
                        pout = int(ps_match.group(1))
                        if 10 < pout < 500:  # Sanity check for home miners
                            r["power_watts"] = pout
    # Get pool info to extract worker name for pool share validation
    r["stratum_user"], r["worker_name"] = _cgminer_get_worker(ip, port, timeout)
    return r if r["hashrate_ghs"] is not None else None


def fetch_futurebit(ip, port=4028, timeout=5):
    """
    Fetch data from FutureBit Apollo miners via BFGMiner API (CGMiner-compatible).
    Supports: FutureBit Apollo (2-3.8 TH/s), Apollo II (6-9 TH/s)

    FutureBit Apollo is a desktop home miner with built-in Bitcoin full node.
    Uses BFGMiner internally which exposes CGMiner-compatible API on port 4028.

    Note: Apollo also has a GraphQL API on port 5000 (/graphql) for the web UI,
    but we use the standard mining API for compatibility.

    Reference: https://github.com/jstefanop/apolloapi-v2
    """
    r = {"hashrate_ghs": None, "found_blocks": 0, "temps": {}, "uptime": None, "accepted": 0, "rejected": 0, "stale": 0, "stratum_user": "", "worker_name": ""}
    d = _cgminer(ip, port, "summary", timeout)
    if d and "SUMMARY" in d:
        for s in d["SUMMARY"]:
            # FutureBit Apollo reports in MH/s or GH/s
            ghs = s.get("GHS av", 0) or s.get("GHS 5s", 0)
            if ghs > 0:
                r["hashrate_ghs"] = float(ghs)
            else:
                mhs = s.get("MHS av", 0) or s.get("MHS 5s", 0)
                if mhs > 0:
                    r["hashrate_ghs"] = mhs / 1000
            r.update({
                "found_blocks": s.get("Found Blocks", 0),
                "uptime": s.get("Elapsed"),
                "accepted": s.get("Accepted", 0),
                "rejected": s.get("Rejected", 0),
                "stale": s.get("Stale", 0)
            })
    st = _cgminer(ip, port, "stats", timeout)
    if st and "STATS" in st:
        for s in st["STATS"]:
            # Standard CGMiner temp fields
            for k, t in [("temp", "chip"), ("temp2", "board"), ("Temperature", "chip")]:
                if k in s and s[k]:
                    r["temps"][t] = s[k]
    r["stratum_user"], r["worker_name"] = _cgminer_get_worker(ip, port, timeout)
    return r if r["hashrate_ghs"] is not None else None


def fetch_antminer(ip, port=4028, timeout=5):
    """
    Fetch data from Bitmain Antminer via CGMiner API.
    Supports: S19, S19 Pro, S19j Pro, S19 XP, S21, T21, etc.
    Reference: https://github.com/bitmaintech/cgminer
    """
    r = {"hashrate_ghs": None, "found_blocks": 0, "temps": {}, "uptime": None, "accepted": 0, "rejected": 0, "stale": 0, "hw_errors": 0, "power_watts": 0, "fans": [], "chain_hashrates": [], "stratum_user": "", "worker_name": ""}
    d = _cgminer(ip, port, "summary", timeout)
    if d and "SUMMARY" in d:
        for s in d["SUMMARY"]:
            # Antminer reports in GH/s for newer models
            ghs = s.get("GHS av", 0) or s.get("GHS 5s", 0)
            if not ghs:
                mhs = s.get("MHS av", 0) or s.get("MHS 5s", 0)
                ghs = mhs / 1000 if mhs else 0
            if ghs > 0: r["hashrate_ghs"] = float(ghs)
            r.update({"found_blocks": s.get("Found Blocks", 0), "uptime": s.get("Elapsed"), "accepted": s.get("Accepted", 0), "rejected": s.get("Rejected", 0), "stale": s.get("Stale", 0), "hw_errors": s.get("Hardware Errors", 0)})
    st = _cgminer(ip, port, "stats", timeout)
    if st and "STATS" in st:
        for s in st["STATS"]:
            # Temperatures: temp1, temp2, temp3 are chip temps; temp2_1, temp2_2, temp2_3 are PCB
            chip_temps = [s.get(f"temp{i}", 0) for i in range(1, 4) if s.get(f"temp{i}", 0) > 0]
            if chip_temps: r["temps"]["chip"] = max(chip_temps)
            pcb_temps = [s.get(f"temp2_{i}", 0) for i in range(1, 4) if s.get(f"temp2_{i}", 0) > 0]
            if pcb_temps: r["temps"]["board"] = max(pcb_temps)
            # Inlet/outlet
            if s.get("temp_inlet"): r["temps"]["inlet"] = s["temp_inlet"]
            if s.get("temp_outlet"): r["temps"]["outlet"] = s["temp_outlet"]
            # Fans
            fans = [s.get(f"fan{i}", 0) for i in range(1, 5) if s.get(f"fan{i}", 0) > 0]
            if fans: r["fans"] = fans
            # Power
            if s.get("Power"): r["power_watts"] = s["Power"]
            # Per-chain hashrates (hashboard health) - chain_rate1, chain_rate2, chain_rate3 in GH/s
            chains = [s.get(f"chain_rate{i}", 0) for i in range(1, 4)]
            # Also try chain_rateideal format and rate_ideal format for different firmware
            if not any(c for c in chains):
                chains = [s.get(f"chain_rateideal{i}", 0) for i in range(1, 4)]
            # Convert string values to float (some firmware returns "1234.56")
            parsed = []
            for c in chains:
                try:
                    parsed.append(float(c) if c else 0)
                except (ValueError, TypeError):
                    parsed.append(0)
            if any(p > 0 for p in parsed):
                r["chain_hashrates"] = parsed
    r["stratum_user"], r["worker_name"] = _cgminer_get_worker(ip, port, timeout)
    return r if r["hashrate_ghs"] is not None else None


def fetch_whatsminer(ip, port=4028, timeout=5):
    """
    Fetch data from MicroBT Whatsminer via CGMiner API.
    Supports: M30S, M30S+, M30S++, M50, M50S, M60, M60S, etc.
    Note: CGMiner API must be enabled in miner web interface.
    Reference: https://www.whatsminer.com/file/WhatsminerAPI%20V2.0.3.pdf
    """
    r = {"hashrate_ghs": None, "found_blocks": 0, "temps": {}, "uptime": None, "accepted": 0, "rejected": 0, "stale": 0, "hw_errors": 0, "power_watts": 0, "fans": [], "chain_hashrates": [], "stratum_user": "", "worker_name": ""}
    d = _cgminer(ip, port, "summary", timeout)
    if d and "SUMMARY" in d:
        for s in d["SUMMARY"]:
            ghs = s.get("GHS av", 0) or s.get("GHS 5s", 0)
            if not ghs:
                mhs = s.get("MHS av", 0) or s.get("MHS 5s", 0)
                ghs = mhs / 1000 if mhs else 0
            if ghs > 0: r["hashrate_ghs"] = float(ghs)
            r.update({"found_blocks": s.get("Found Blocks", 0), "uptime": s.get("Elapsed"), "accepted": s.get("Accepted", 0), "rejected": s.get("Rejected", 0), "stale": s.get("Stale", 0), "hw_errors": s.get("Hardware Errors", 0)})
    st = _cgminer(ip, port, "stats", timeout)
    if st and "STATS" in st:
        for s in st["STATS"]:
            # Whatsminer temps
            temps = [s.get(f"temp{i}", 0) for i in range(1, 4) if s.get(f"temp{i}", 0) > 0]
            if temps: r["temps"]["chip"] = max(temps)
            # Fans
            fans = [s.get(f"fan{i}", 0) for i in range(1, 5) if s.get(f"fan{i}", 0) > 0]
            if fans: r["fans"] = fans
            # Power
            if s.get("Power"): r["power_watts"] = s["Power"]
            if s.get("Power_RT"): r["power_watts"] = s["Power_RT"]
            # Per-chain hashrates (hashboard health)
            chains = [s.get(f"chain_rate{i}", 0) for i in range(1, 4)]
            parsed = []
            for c in chains:
                try:
                    parsed.append(float(c) if c else 0)
                except (ValueError, TypeError):
                    parsed.append(0)
            if any(p > 0 for p in parsed):
                r["chain_hashrates"] = parsed
    r["stratum_user"], r["worker_name"] = _cgminer_get_worker(ip, port, timeout)
    return r if r["hashrate_ghs"] is not None else None


def fetch_innosilicon(ip, port=4028, timeout=5):
    """
    Fetch data from Innosilicon via CGMiner API.
    Supports: A10, A10 Pro, A11, T2T, T3, etc.
    """
    r = {"hashrate_ghs": None, "found_blocks": 0, "temps": {}, "uptime": None, "accepted": 0, "rejected": 0, "stale": 0, "power_watts": 0, "fans": [], "stratum_user": "", "worker_name": ""}
    d = _cgminer(ip, port, "summary", timeout)
    if d and "SUMMARY" in d:
        for s in d["SUMMARY"]:
            ghs = s.get("GHS av", 0) or s.get("GHS 5s", 0)
            if not ghs:
                mhs = s.get("MHS av", 0) or s.get("MHS 5s", 0)
                ghs = mhs / 1000 if mhs else 0
            if ghs > 0: r["hashrate_ghs"] = float(ghs)
            r.update({"found_blocks": s.get("Found Blocks", 0), "uptime": s.get("Elapsed"), "accepted": s.get("Accepted", 0), "rejected": s.get("Rejected", 0), "stale": s.get("Stale", 0)})
    st = _cgminer(ip, port, "stats", timeout)
    if st and "STATS" in st:
        for s in st["STATS"]:
            # Innosilicon temps
            temps = [s.get(f"temp{i}", 0) for i in range(1, 10) if s.get(f"temp{i}", 0) > 0]
            if temps: r["temps"]["chip"] = max(temps)
            board_temps = [s.get(f"temp2_{i}", 0) for i in range(1, 10) if s.get(f"temp2_{i}", 0) > 0]
            if board_temps: r["temps"]["board"] = max(board_temps)
            # Fans
            fans = [s.get(f"fan{i}", 0) for i in range(1, 5) if s.get(f"fan{i}", 0) > 0]
            if fans: r["fans"] = fans
            # Power
            if s.get("Power"): r["power_watts"] = s["Power"]
    r["stratum_user"], r["worker_name"] = _cgminer_get_worker(ip, port, timeout)
    return r if r["hashrate_ghs"] is not None else None


# BraiinsOS session cache for token reuse
_braiins_sessions = {}  # {ip: {"token": str, "expires": timestamp}}

def fetch_braiins(ip, username="root", password="", timeout=10):
    """Fetch data from BraiinsOS/BOS+ miner via Public REST API (v1).

    Supported devices: Antminer S9, S17, S19, S21, T17, T19 running BraiinsOS.
    REST API base: /api/v1/ on port 80 (gRPC-to-REST gateway).
    Auth: POST /api/v1/auth/login → Bearer token.
    Reference: https://developer.braiins-os.com/latest/openapi.html
    """
    r = {"hashrate_ghs": None, "found_blocks": 0, "temps": {}, "uptime": None,
         "accepted": 0, "rejected": 0, "stale": 0, "power_watts": 0, "fans": [],
         "stratum_user": "", "worker_name": ""}

    if not REQUESTS_AVAILABLE:
        logger.debug("fetch_braiins: 'requests' module not installed — skipping")
        return r

    try:
        if not validate_miner_ip(ip):
            return None

        base_url = f"http://{ip}"
        now = time.time()
        session = _braiins_sessions.get(ip, {})
        token = session.get("token")
        expires = session.get("expires", 0)

        # Authenticate via /api/v1/auth/login
        if not token or now >= expires:
            auth_resp = requests.post(f"{base_url}/api/v1/auth/login", json={"username": username, "password": password}, timeout=timeout)
            if auth_resp.status_code == 200:
                auth_data = auth_resp.json()
                token = auth_data.get("token", "")
                ttl = auth_data.get("timeout_s", 3600)
                _braiins_sessions[ip] = {"token": token, "expires": now + ttl - 60}
            else:
                token = None

        headers = {"Authorization": f"Bearer {token}"} if token else {}

        # Fetch mining stats: GET /api/v1/miner/stats
        try:
            stats_resp = requests.get(f"{base_url}/api/v1/miner/stats", headers=headers, timeout=timeout)
            if stats_resp.status_code == 200:
                stats = stats_resp.json()
                # Hashrate: miner_stats.nominal_hashrate.gigahash_per_second (already GH/s)
                miner_stats = stats.get("miner_stats", {})
                nominal = miner_stats.get("nominal_hashrate", {})
                ghs = nominal.get("gigahash_per_second", 0)
                if not ghs:
                    # Fallback to real_hashrate.last_5m
                    real = miner_stats.get("real_hashrate", {})
                    last5m = real.get("last_5m", {})
                    ghs = last5m.get("gigahash_per_second", 0)
                if ghs and ghs > 0:
                    r["hashrate_ghs"] = float(ghs)
                r["found_blocks"] = miner_stats.get("found_blocks", 0)
                # Power: power_stats.approximated_consumption.watt
                power_stats = stats.get("power_stats", {})
                consumption = power_stats.get("approximated_consumption", {})
                r["power_watts"] = consumption.get("watt", 0)
                # Shares: pool_stats
                pool_stats = stats.get("pool_stats", {})
                r["accepted"] = pool_stats.get("accepted_shares", 0)
                r["rejected"] = pool_stats.get("rejected_shares", 0)
                r["stale"] = pool_stats.get("stale_shares", 0)
        except Exception:
            pass

        # Fetch cooling: GET /api/v1/cooling/state
        try:
            cool_resp = requests.get(f"{base_url}/api/v1/cooling/state", headers=headers, timeout=timeout)
            if cool_resp.status_code == 200:
                cooling = cool_resp.json()
                # Temperature: highest_temperature.temperature.degree_c
                highest = cooling.get("highest_temperature", {})
                temp_obj = highest.get("temperature", {})
                degree_c = temp_obj.get("degree_c", 0)
                if degree_c and degree_c > 0:
                    r["temps"]["chip"] = degree_c
                # Fans: fans[].rpm
                fans_data = cooling.get("fans", [])
                r["fans"] = [f.get("rpm", 0) for f in fans_data if f.get("rpm")]
        except Exception:
            pass

        # Fetch miner details: GET /api/v1/miner/details
        try:
            det_resp = requests.get(f"{base_url}/api/v1/miner/details", headers=headers, timeout=timeout)
            if det_resp.status_code == 200:
                details = det_resp.json()
                r["uptime"] = details.get("bosminer_uptime_s", 0) or details.get("system_uptime_s", 0)
        except Exception:
            pass

        return r if r["hashrate_ghs"] is not None else None

    except Exception as e:
        logger.debug(f"fetch_braiins({ip}): {e}")
        return None


# Vnish session cache for token reuse
_vnish_sessions = {}  # {ip: {"token": str, "expires": timestamp}}

def fetch_vnish(ip, password="admin", timeout=10):
    """Fetch data from Vnish firmware miner via REST API on port 80.

    Supported devices: Antminer S9, S17, S19, S21 running Vnish firmware.
    Web API on port 80: /api/v1/unlock, /api/v1/summary, /api/v1/metrics
    Also exposes CGMiner-compatible RPC on port 4028 (used for hashrate).
    Reference: https://vnish.group/ | pyasic vnish backend
    """
    r = {"hashrate_ghs": None, "found_blocks": 0, "temps": {}, "uptime": None,
         "accepted": 0, "rejected": 0, "stale": 0, "power_watts": 0, "fans": [],
         "stratum_user": "", "worker_name": ""}

    if not REQUESTS_AVAILABLE:
        logger.debug("fetch_vnish: 'requests' module not installed — skipping")
        return r

    try:
        if not validate_miner_ip(ip):
            return None

        base_url = f"http://{ip}"
        now = time.time()
        session = _vnish_sessions.get(ip, {})
        token = session.get("token")
        expires = session.get("expires", 0)

        # Authenticate via /api/v1/unlock (port 80)
        if not token or now >= expires:
            auth_resp = requests.post(f"{base_url}/api/v1/unlock", json={"pw": password}, timeout=timeout)
            if auth_resp.status_code == 200:
                token = auth_resp.json().get("token", "")
                _vnish_sessions[ip] = {"token": token, "expires": now + 3000}
            else:
                token = None

        # Vnish uses plain token (not Bearer) for data endpoints
        headers = {"Authorization": token} if token else {}

        # Fetch summary: miner.power_usage, miner.miner_status, system info
        try:
            sum_resp = requests.get(f"{base_url}/api/v1/summary", headers=headers, timeout=timeout)
            if sum_resp.status_code == 200:
                summary = sum_resp.json()
                miner_data = summary.get("miner", {})
                # Power from summary.miner.power_usage
                r["power_watts"] = miner_data.get("power_usage", 0)
                # Chains temperature data
                chains = miner_data.get("chains", [])
                chip_temps = []
                for chain in chains:
                    status = chain.get("status", {})
                    # Some firmware versions nest temps in chain data
                    temp = chain.get("temp", 0) or chain.get("chip_temp", 0)
                    if temp and temp > 0:
                        chip_temps.append(temp)
                if chip_temps:
                    r["temps"]["chip"] = max(chip_temps)
                # Uptime may be in summary
                r["uptime"] = summary.get("uptime", 0)
        except Exception:
            pass

        # Fetch hashrate via CGMiner RPC on port 4028 (more reliable than web API)
        try:
            d = _cgminer(ip, 4028, "summary", timeout)
            if d and "SUMMARY" in d:
                for s in d["SUMMARY"]:
                    ghs = s.get("GHS 5s", 0) or s.get("GHS av", 0)
                    if not ghs:
                        mhs = s.get("MHS 5s", 0) or s.get("MHS av", 0)
                        ghs = mhs / 1000 if mhs else 0
                    if ghs and float(ghs) > 0:
                        r["hashrate_ghs"] = float(ghs)
                    r["accepted"] = s.get("Accepted", 0)
                    r["rejected"] = s.get("Rejected", 0)
                    r["found_blocks"] = s.get("Found Blocks", 0)
                    if not r["uptime"]:
                        r["uptime"] = s.get("Elapsed")
            # Fans and detailed temps from CGMiner stats
            st = _cgminer(ip, 4028, "stats", timeout)
            if st and "STATS" in st:
                for s in st["STATS"]:
                    chip_t = [s.get(f"temp{i}", 0) for i in range(1, 4) if s.get(f"temp{i}", 0) > 0]
                    if chip_t and not r["temps"].get("chip"):
                        r["temps"]["chip"] = max(chip_t)
                    pcb_t = [s.get(f"temp2_{i}", 0) for i in range(1, 4) if s.get(f"temp2_{i}", 0) > 0]
                    if pcb_t:
                        r["temps"]["board"] = max(pcb_t)
                    fans = [s.get(f"fan{i}", 0) for i in range(1, 5) if s.get(f"fan{i}", 0) > 0]
                    if fans:
                        r["fans"] = fans
        except Exception:
            pass

        # Fallback: try web API hr_nominal if CGMiner didn't return hashrate
        if r["hashrate_ghs"] is None:
            try:
                sum_resp = requests.get(f"{base_url}/api/v1/summary", headers=headers, timeout=timeout)
                if sum_resp.status_code == 200:
                    summary = sum_resp.json()
                    hr_ths = summary.get("hr_nominal", 0)
                    if hr_ths:
                        r["hashrate_ghs"] = hr_ths * 1000  # TH/s to GH/s
            except Exception:
                pass

        # Fetch metrics (power fallback if not from summary)
        if not r["power_watts"]:
            try:
                met_resp = requests.get(f"{base_url}/api/v1/metrics", headers=headers, timeout=timeout)
                if met_resp.status_code == 200:
                    metrics = met_resp.json()
                    r["power_watts"] = metrics.get("power_consumption", 0) or metrics.get("power", 0)
            except Exception:
                pass

        return r if r["hashrate_ghs"] is not None else None

    except Exception as e:
        logger.debug(f"fetch_vnish({ip}): {e}")
        return None


def fetch_epic(ip, port=4028, username="root", password="letmein", timeout=10):
    """Fetch data from ePIC BlockMiner via HTTP REST API on port 4028.

    IMPORTANT: ePIC uses HTTP REST on port 4028, NOT CGMiner TCP socket.
    Endpoints: /summary, /hashrate, /fanspeed, /capabilities
    Default credentials: root / letmein
    Reference: https://github.com/epicblockchain/epic-miner | pyasic epic backend
    """
    r = {"hashrate_ghs": None, "found_blocks": 0, "temps": {}, "uptime": None,
         "accepted": 0, "rejected": 0, "stale": 0, "power_watts": 0, "fans": [],
         "stratum_user": "", "worker_name": ""}

    if not REQUESTS_AVAILABLE:
        logger.debug("fetch_epic: 'requests' module not installed — skipping")
        return r

    try:
        if not validate_miner_ip(ip):
            return None

        base_url = f"http://{ip}:{port}"

        # Fetch summary (main stats endpoint)
        try:
            sum_resp = requests.get(f"{base_url}/summary", auth=(username, password), timeout=timeout)
            if sum_resp.status_code == 200:
                summary = sum_resp.json()
                # Summary may contain Software, Mining, Stratum, Session, HBs arrays
                mining = summary.get("Mining", {})
                if isinstance(summary, dict):
                    # Hashrate in GH/s or TH/s depending on model
                    hr = mining.get("Speed(GHS)", 0) or mining.get("GHS av", 0)
                    if hr and float(hr) > 0:
                        r["hashrate_ghs"] = float(hr)
                    r["accepted"] = mining.get("Accepted", 0)
                    r["rejected"] = mining.get("Rejected", 0)
                    # Uptime
                    session = summary.get("Session", {})
                    r["uptime"] = session.get("Uptime", 0) or session.get("Elapsed", 0)
                    # Stratum info
                    stratum = summary.get("Stratum", {})
                    user = stratum.get("Current User", "")
                    if user and "." in user:
                        parts = user.split(".", 1)
                        r["stratum_user"] = user  # Full wallet.worker for pool matching
                        r["worker_name"] = parts[1]
                    elif user:
                        r["stratum_user"] = user
                    # Hashboards temps
                    hbs = summary.get("HBs", [])
                    if isinstance(hbs, list):
                        chip_temps = []
                        for hb in hbs:
                            temp = hb.get("Temperature", 0) or hb.get("Chip Temp", 0)
                            if temp and float(temp) > 0:
                                chip_temps.append(float(temp))
                        if chip_temps:
                            r["temps"]["chip"] = max(chip_temps)
        except Exception:
            pass

        # Fetch fan speeds
        try:
            fan_resp = requests.get(f"{base_url}/fanspeed", auth=(username, password), timeout=timeout)
            if fan_resp.status_code == 200:
                fan_data = fan_resp.json()
                if isinstance(fan_data, dict):
                    fans = fan_data.get("Fans", [])
                    if isinstance(fans, list):
                        r["fans"] = [f.get("RPM", 0) for f in fans if f.get("RPM", 0) > 0]
        except Exception:
            pass

        # Fetch capabilities (power info)
        try:
            cap_resp = requests.get(f"{base_url}/capabilities", auth=(username, password), timeout=timeout)
            if cap_resp.status_code == 200:
                caps = cap_resp.json()
                if isinstance(caps, dict):
                    r["power_watts"] = caps.get("Power Consumption", 0) or caps.get("Power", 0)
        except Exception:
            pass

        return r if r["hashrate_ghs"] is not None else None

    except Exception as e:
        logger.debug(f"fetch_epic({ip}): {e}")
        return None


# === POOL SHARE VALIDATION ===
# Verifies miners are actually submitting valid shares to the pool (not just claiming to)

def fetch_pool_miners():
    """Fetch miner stats from Spiral Stratum pool API.

    Returns tuple: (miners_dict, has_per_worker_data)
    - miners_dict: {worker_id: stats} mapping
    - has_per_worker_data: True if pool returns per-worker breakdown, False if wallet-level only

    Pool APIs may return either:
    - Per-worker: [{"miner": "wallet.worker1", ...}, {"miner": "wallet.worker2", ...}]
    - Wallet-level only: [{"address": "wallet", ...}] (no per-worker breakdown)

    When wallet-level only, zombie detection based on pool shares should be skipped
    to avoid false alerts (we can't verify individual miners against pool).
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = CONFIG.get("pool_id", "")
        if not pool_id:
            logger.error("pool_id must be configured in config file for fetch_pool_miners()")
            return {}, False
        url = f"{pool_url}/api/pools/{pool_id}/miners"
        data = _http(url, timeout=10)
        if data and isinstance(data, list):
            # Try "miner" field first (per-worker format), fall back to "address" (wallet-level)
            miners = {}
            has_per_worker = False
            for m in data:
                # Per-worker format uses "miner" field with wallet.worker format
                miner_id = m.get("miner", "")
                if miner_id and "." in miner_id:
                    has_per_worker = True
                    miners[miner_id] = m
                elif not miner_id:
                    # Wallet-level format uses "address" field
                    addr = m.get("address", "")
                    if addr:
                        miners[addr] = m
            return miners, has_per_worker
        return {}, False
    except (KeyError, TypeError, AttributeError):
        return {}, False


# Cache for pool connections (to avoid hammering the API)
_pool_connections_cache = {
    "connections": {"by_ip": {}, "by_worker": {}},  # Dual-indexed: IP and worker name
    "last_update": 0
}


def fetch_pool_connections():
    """Fetch active stratum connections from pool API.

    Returns dict with two indexes:
      - "by_ip": maps IP addresses to connection info
      - "by_worker": maps worker names to connection info
    Used to detect online status for devices without HTTP API (ESP32 Miner V2).

    Requires pool_admin_api_key to be configured (connections endpoint is admin-only).
    """
    global _pool_connections_cache

    # Rate limit to every 30 seconds
    if time.time() - _pool_connections_cache["last_update"] < 30:
        return _pool_connections_cache["connections"]

    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = CONFIG.get("pool_id", "")
        admin_key = CONFIG.get("pool_admin_api_key", "")

        if not pool_id:
            return _pool_connections_cache["connections"]

        headers = {}
        if admin_key:
            headers["X-API-Key"] = admin_key

        url = f"{pool_url}/api/pools/{pool_id}/connections?limit=1000"
        data = _http(url, timeout=10, headers=headers)

        if data and isinstance(data, dict) and "connections" in data:
            by_ip = {}
            by_worker = {}
            for conn in data["connections"]:
                conn_info = {
                    "workerName": conn.get("workerName", ""),
                    "minerAddress": conn.get("minerAddress", ""),
                    "userAgent": conn.get("userAgent", ""),
                    "difficulty": conn.get("difficulty", 0),
                    "shareCount": conn.get("shareCount", 0),
                    "connectedAt": conn.get("connectedAt", ""),
                    "lastActivity": conn.get("lastActivity", ""),
                }

                remote_addr = conn.get("remoteAddr", "")
                if remote_addr:
                    # Extract just the IP (remove port if present)
                    # Handle IPv6 bracketed [::1]:port and IPv4 1.2.3.4:port
                    if remote_addr.startswith("["):
                        # IPv6 bracketed: [::1]:8080
                        bracket_end = remote_addr.find("]")
                        ip = remote_addr[1:bracket_end] if bracket_end > 0 else remote_addr[1:]
                    elif remote_addr.count(":") == 1:
                        # IPv4 with port: 1.2.3.4:8080
                        ip = remote_addr.split(":")[0]
                    else:
                        # Bare IPv4 or bare IPv6 (no port)
                        ip = remote_addr
                    if ip:
                        by_ip[ip] = conn_info

                # Also index by worker name for devices with masked/unknown IPs
                worker = conn.get("workerName", "")
                if worker:
                    by_worker[worker] = conn_info

            _pool_connections_cache["connections"] = {"by_ip": by_ip, "by_worker": by_worker}
            _pool_connections_cache["last_update"] = time.time()

        return _pool_connections_cache["connections"]

    except Exception as e:
        logger.debug(f"Error fetching pool connections: {e}")
        return _pool_connections_cache["connections"]


def fetch_pool_worker_stats(worker_name, miner_address=None):
    """Fetch worker stats from pool API.

    Used for devices without HTTP API (ESP32 Miner V2) to get hashrate/share stats.

    Args:
        worker_name: The stratum worker name (e.g., "cardy")
        miner_address: Optional wallet address. If not provided, uses first configured address.

    Returns:
        Dict with worker stats or None if not available
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = CONFIG.get("pool_id", "")

        if not pool_id:
            return None

        # If no miner address provided, try to get from config
        if not miner_address:
            # Try pool_addresses first (multi-coin), then legacy pool_address
            pool_addresses = CONFIG.get("pool_addresses", {})
            if pool_addresses:
                for coin, addr in pool_addresses.items():
                    if addr:
                        miner_address = addr
                        break
            if not miner_address:
                miner_address = CONFIG.get("pool_address", "")

        if not miner_address:
            return None

        # URL-encode the worker name and address
        from urllib.parse import quote
        safe_name = quote(worker_name, safe='')
        safe_address = quote(miner_address, safe='')

        url = f"{pool_url}/api/pools/{pool_id}/miners/{safe_address}/workers/{safe_name}"
        data = _http(url, timeout=10)

        if data and isinstance(data, dict):
            return data

        return None

    except Exception as e:
        logger.debug(f"Error fetching pool worker stats for {worker_name}: {e}")
        return None


def fetch_esp32miner_from_pool(ip, name):
    """Fetch ESP32 Miner stats from pool API (since ESP32 Miner V2 has no HTTP API).

    ESP32 Miner V2 devices don't expose any HTTP API - they only communicate via Stratum.
    We must poll the pool's connections and worker stats APIs to get data.

    Lookup strategy (in order):
      1. Match by IP address (requires unmasked IPs from admin API)
      2. Match by worker name (the configured name/nickname for the device)

    Args:
        ip: ESP32 Miner IP address
        name: Configured name for the device (typically the stratum worker name)

    Returns:
        Dict with miner stats compatible with process() function, or None if not connected
    """
    connections = fetch_pool_connections()
    by_ip = connections.get("by_ip", {})
    by_worker = connections.get("by_worker", {})

    # Try IP match first (works when admin API returns unmasked IPs)
    conn = by_ip.get(ip)

    # Fall back to worker name match (works even with masked IPs)
    if not conn and name:
        conn = by_worker.get(name)

    if not conn:
        # Device not found in pool connections - offline
        return None

    worker_name = conn.get("workerName", "")
    miner_address = conn.get("minerAddress", "")

    # Build base result from connection data
    result = {
        "online": True,
        "stratum_connected": True,
        "no_http_api": True,
        "hashrate_ghs": 0,
        "power_watts": 2,  # Default 2W for ESP32
        "temps": {},
        "uptime": 0,
        "accepted": conn.get("shareCount", 0),
        "rejected": 0,
        "stale": 0,
        "pool_url": "",
        "hostname": name or worker_name or ip,
        "stratum_user": f"{miner_address}.{worker_name}" if miner_address and worker_name else "",
        "worker_name": worker_name,
        "current_difficulty": conn.get("difficulty", 0),
        "user_agent": conn.get("userAgent", ""),
    }

    # Try to get detailed worker stats from pool
    if worker_name:
        pool_stats = fetch_pool_worker_stats(worker_name, miner_address)
        if pool_stats:
            # Pool hashrate is typically in H/s, convert to GH/s
            current_hr = pool_stats.get("currentHashrate", 0)
            avg_hr = pool_stats.get("averageHashrate", 0)
            result["hashrate_ghs"] = (current_hr or avg_hr) / 1e9
            result["accepted"] = pool_stats.get("sharesAccepted", result["accepted"])
            result["rejected"] = pool_stats.get("sharesRejected", 0)
            result["current_difficulty"] = pool_stats.get("difficulty", result["current_difficulty"])

    return result


def fetch_pool_stats():
    """Fetch pool stats from Spiral Stratum API.
    Uses /api/pools endpoint to get all pools, then finds the matching one.
    This avoids the /api/pools/{pool_id} endpoint which has regex validation issues
    with underscores in pool IDs like 'dgb_sha256_1'.

    V2 multi-coin mode: If pool_id is not configured, returns the first pool
    with a valid blockHeight (for sync detection).
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = CONFIG.get("pool_id", "")

        # Use /api/pools to get all pools (bypasses pool ID regex validation)
        url = f"{pool_url}/api/pools"
        data = _http(url, timeout=10)

        if data and isinstance(data, dict) and "pools" in data:
            pools = data["pools"]

            # If pool_id is configured, find that specific pool
            if pool_id:
                for pool in pools:
                    if pool.get("id") == pool_id:
                        return pool

            # V2 multi-coin fallback: return first pool with valid blockHeight
            # This ensures sync detection works even without explicit pool_id config
            for pool in pools:
                block_height = pool.get("poolStats", {}).get("blockHeight", 0)
                if block_height > 0:
                    return pool

            # Last resort: return first pool if any exist
            if pools:
                if len(pools) > 1:
                    logger.warning(f"fetch_pool_stats() fallback: No pool_id configured and no pool with blockHeight > 0, returning first pool arbitrarily from {len(pools)} pools")
                return pools[0]

        # Fallback: return None if no pools found
        return None
    except (KeyError, TypeError):
        return None

def fetch_miner_pool_stats(worker_name):
    """Fetch specific miner stats from pool API"""
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = CONFIG.get("pool_id", "")
        if not pool_id:
            logger.error("pool_id must be configured in config file for fetch_miner_pool_stats()")
            return None
        # Spiral Stratum uses wallet.worker format
        wallet = CONFIG.get("wallet_address", "")
        worker_id = f"{wallet}.{worker_name}" if wallet else worker_name
        url = f"{pool_url}/api/pools/{pool_id}/miners/{worker_id}"
        return _http(url, timeout=10)
    except (KeyError, TypeError):
        return None


def fetch_pool_blocks(limit=20, pool_id=None):
    """Fetch found blocks from the pool API with status information.

    P0 AUDIT FIX: Added to enable orphan detection in Sentinel.
    The pool API returns block status (pending, confirmed, orphaned).

    Args:
        limit: Maximum number of blocks to fetch (default 20)
        pool_id: Optional pool ID to query. If None, uses pool_id from config.

    Returns:
        List of block dicts with keys: height, hash, status, created, miner, worker, coin
        Returns empty list on error.
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        if pool_id is None:
            pool_id = CONFIG.get("pool_id", "")
        if not pool_id:
            logger.error("pool_id must be configured in config file for fetch_pool_blocks()")
            return []
        url = f"{pool_url}/api/pools/{pool_id}/blocks?pageSize={limit}"
        data = _http(url, timeout=10)
        if data and isinstance(data, list):
            return data
        return []
    except (KeyError, TypeError, Exception) as e:
        logger.debug(f"fetch_pool_blocks error: {e}")
        return []


def get_pool_share_stats():
    """Get share statistics from pool for all known workers.

    Returns tuple: (worker_shares, has_per_worker_data)
    - worker_shares: {worker_name: stats} mapping
    - has_per_worker_data: True if pool supports per-worker breakdown

    When has_per_worker_data is False, callers should NOT use this data for
    zombie detection (tracking individual miners with zeros causes false alerts).
    The aggregate wallet-level data is still useful for dashboard/API stats.
    """
    pool_miners, has_per_worker = fetch_pool_miners()
    wallet = CONFIG.get("wallet_address", "")

    worker_shares = {}
    for miner_id, stats in pool_miners.items():
        # Extract worker name from wallet.worker format
        worker = miner_id.split(".")[-1] if "." in miner_id else miner_id
        # Only track workers from our wallet
        if wallet and not miner_id.startswith(wallet):
            continue
        worker_shares[worker] = {
            "hashrate": stats.get("hashrate", 0),
            "shares_per_second": stats.get("sharesPerSecond", 0),
            "last_share": stats.get("lastShare") or stats.get("lastPayment"),  # Prefer lastShare; fall back to lastPayment
        }
    return worker_shares, has_per_worker

# Module-level lookups populated by get_total_hashrate() each cycle
_miner_ip_lookup = {}    # Maps display name -> IP address
_miner_type_lookup = {}  # Maps display name -> miner type string (e.g., "antminer", "axeos")

def get_total_hashrate():
    """Get total hashrate from all configured miners with algorithm-aware formatting."""
    global _avalon_display_names, _miner_ip_lookup, _miner_type_lookup
    _avalon_display_names = set()  # Clear and rebuild on each collection cycle
    _miner_ip_lookup = {}   # Maps display name -> IP address
    _miner_type_lookup = {} # Maps display name -> miner type string
    total, details, temps, status, power, uptimes, mblocks, mpools, mstats, worker_names = 0, {}, {}, {}, {}, {}, {}, {}, {}, {}
    fans = {}  # Maps display name -> list of fan RPMs
    chain_data = {}  # Maps display name -> list of per-chain hashrates (GH/s)
    hw_errors_data = {}  # Maps display name -> total HW error count
    # Get the primary coin to determine algorithm for formatting
    primary_coin = get_primary_coin()
    def process(name, data, exp, watts=0, has_blocks=False, miner_ip=None, miner_type=None):
        nonlocal total
        # Populate IP and type lookups for thermal protection and fan alerts
        if miner_ip:
            _miner_ip_lookup[name] = miner_ip
        if miner_type:
            _miner_type_lookup[name] = miner_type
        # A miner is "online" if it has hashrate OR is confirmed connected via stratum
        # (ESP32 miners via pool API set stratum_connected=True but may report 0 hashrate
        # between their infrequent share submissions — they're still online)
        has_hashrate = data and data.get("hashrate_ghs", 0) > 0
        stratum_connected = data and data.get("stratum_connected", False)
        if has_hashrate or stratum_connected:
            ghs = data.get("hashrate_ghs", 0) if data else 0
            w = data.get("power_watts", watts) if data else watts
            total += ghs; power[name] = w
            if data.get("temps"): temps[name] = data["temps"]
            if data.get("uptime") is not None: uptimes[name] = data["uptime"]
            if data.get("pool_url"): mpools[name] = data["pool_url"]
            if has_blocks: mblocks[name] = data.get("found_blocks", 0)
            if data.get("accepted") is not None: mstats[name] = {"accepted": data.get("accepted", 0), "rejected": data.get("rejected", 0), "stale": data.get("stale", 0)}
            # Extract fan RPM data (available from antminer, whatsminer, innosilicon, goldshell)
            if data.get("fans"):
                fans[name] = data["fans"]
            # Extract per-chain hashrates for hashboard death detection (antminer, whatsminer)
            if data.get("chain_hashrates"):
                chain_data[name] = data["chain_hashrates"]
            # Extract hardware error count for HW error rate tracking
            if data.get("hw_errors") is not None and data["hw_errors"] > 0:
                hw_errors_data[name] = data["hw_errors"]
            # Cache API-reported hostname for display (if miner reports one)
            if data.get("hostname") and miner_ip:
                cache_api_hostname(miner_ip, data["hostname"])
                cache_api_hostname(name, data["hostname"])
            # Cache worker name from stratum config as display fallback
            if data.get("worker_name") and miner_ip:
                cache_api_worker_name(miner_ip, data["worker_name"])
                cache_api_worker_name(name, data["worker_name"])
            # Track worker name AND hostname for pool share validation
            # Pool matching will try: worker_name -> hostname -> IP -> display name
            worker_names[name] = {
                "worker": data.get("worker_name", ""),
                "hostname": data.get("hostname", ""),
                "ip": miner_ip or "",
            }
            # Use algorithm-aware hashrate formatting
            # NOTE: Power is NOT included in details - it's added separately in report embeds
            # to avoid duplication and allow proper emoji formatting
            if ghs > 0:
                hashrate_str = format_hashrate_ghs(ghs, symbol=primary_coin)
                details[name] = hashrate_str
            elif stratum_connected:
                # ESP32/pool-API miners: connected but no hashrate data yet
                details[name] = "connected (lottery)"
            else:
                details[name] = format_hashrate_ghs(0, symbol=primary_coin)
            status[name] = "low_hashrate" if exp > 0 and ghs < exp * RESTART_DROP_TH else "online"
        else:
            fb = exp or 0
            if fb > 0:
                total += fb
                hashrate_str = format_hashrate_ghs(fb, symbol=primary_coin)
                details[name] = f"{hashrate_str} (fallback)"
                status[name] = "offline"
            elif miner_type == "esp32miner" or (miner_ip and MINER_DB.get("miners", {}).get(miner_ip, {}).get("type") == "esp32miner"):
                # ESP32 miners have no HTTP API — they only communicate via Stratum
                # Don't mark as offline (they're not down, just unpollable without pool admin API)
                # Secondary check: cross-reference raw DB type for miners under wrong by_type key
                details[name] = "Online (Stratum)"
                status[name] = "pool_only"
            else:
                details[name] = "OFFLINE"
                status[name] = "offline"
    for m in MINERS.get("nmaxe", []):
        process(m["name"], fetch_nmaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), miner_ip=m["ip"], miner_type="nmaxe")
    # Generic AxeOS devices (Lucky Miner, Jingle Miner, Zyber, etc.)
    # Uses same API as nmaxe (/api/system/info), has_blocks=False for personal best issue
    for m in MINERS.get("axeos", []):
        process(m["name"], fetch_nmaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), has_blocks=False, miner_ip=m["ip"], miner_type="axeos")
    for m in MINERS.get("nerdqaxe", []):
        # NOTE: has_blocks=False for BitAxe/AxeOS devices because their "found_blocks" counter
        # increments on personal best difficulty shares, NOT actual network blocks.
        # Real block detection should come from the stratum server, not miner-reported counts.
        process(m["name"], fetch_nerdqaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), has_blocks=False, miner_ip=m["ip"], miner_type="nerdqaxe")
    for m in MINERS.get("avalon", []):
        # Avalon Nano 3S uses AxeOS and has the same "personal best = block" issue
        # Only trust block counts from industrial ASICs (Antminer, Whatsminer, etc.)
        data = fetch_avalon(m["ip"], m.get("port", 4028))
        # Use worker_name from stratum auth if configured name is just the IP
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        _avalon_display_names.add(display_name)  # Track for temp alert exclusion
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 0), has_blocks=False, miner_ip=m["ip"], miner_type="avalon")
    # Industrial ASIC miners (Bitmain Antminer, MicroBT Whatsminer, Innosilicon)
    for m in MINERS.get("antminer", []):
        data = fetch_antminer(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 3250), has_blocks=True, miner_ip=m["ip"], miner_type="antminer")
    for m in MINERS.get("whatsminer", []):
        data = fetch_whatsminer(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 3400), has_blocks=True, miner_ip=m["ip"], miner_type="whatsminer")
    for m in MINERS.get("innosilicon", []):
        data = fetch_innosilicon(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 3500), has_blocks=True, miner_ip=m["ip"], miner_type="innosilicon")
    # Goldshell Scrypt miners (Mini DOGE, LT5, LT6, etc.)
    for m in MINERS.get("goldshell", []):
        data = fetch_goldshell(m["ip"], m.get("port", 80))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 2300), has_blocks=True, miner_ip=m["ip"], miner_type="goldshell")
    # PlebSource Hammer Miner (Scrypt, uses AxeOS API)
    for m in MINERS.get("hammer", []):
        process(m["name"], fetch_hammer(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 25), has_blocks=False, miner_ip=m["ip"], miner_type="hammer")
    # ESP32 Miner V2 (ESP32-based solo miner, very low hashrate - lottery miner)
    # ESP32 Miner V2 has NO HTTP API - must poll pool for stats via stratum connections
    for m in MINERS.get("esp32miner", []):
        # Use pool-based fetch since ESP32 Miner has no HTTP API
        data = fetch_esp32miner_from_pool(m["ip"], m["name"])
        process(m["name"], data, m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 2), has_blocks=False, miner_ip=m["ip"], miner_type="esp32miner")
    # QAxe / QAxe+ (quad-ASIC, uses AxeOS API)
    for m in MINERS.get("qaxe", []):
        process(m["name"], fetch_nmaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 80), has_blocks=False, miner_ip=m["ip"], miner_type="qaxe")
    for m in MINERS.get("qaxeplus", []):
        process(m["name"], fetch_nmaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 100), has_blocks=False, miner_ip=m["ip"], miner_type="qaxeplus")
    # Canaan AvalonMiner (SHA-256d, uses CGMiner API)
    for m in MINERS.get("canaan", []):
        data = fetch_avalon(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        _avalon_display_names.add(display_name)  # Track for temp alert exclusion
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 3000), has_blocks=True, miner_ip=m["ip"], miner_type="canaan")
    # Ebang Ebit (SHA-256d, uses CGMiner API)
    for m in MINERS.get("ebang", []):
        data = fetch_avalon(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 2800), has_blocks=True, miner_ip=m["ip"], miner_type="ebang")
    # FutureBit Apollo (SHA-256d, uses CGMiner API)
    for m in MINERS.get("futurebit", []):
        data = fetch_futurebit(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 200), has_blocks=True, miner_ip=m["ip"], miner_type="futurebit")
    # Bitmain Antminer L7/L9 (Scrypt, uses same CGMiner API as SHA256 Antminers)
    for m in MINERS.get("antminer_scrypt", []):
        data = fetch_antminer(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 3250), has_blocks=True, miner_ip=m["ip"], miner_type="antminer_scrypt")
    # BitAxe family (uses same AxeOS HTTP API as nmaxe)
    for m in MINERS.get("bitaxe", []):
        process(m["name"], fetch_nmaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), has_blocks=False, miner_ip=m["ip"], miner_type="bitaxe")
    # NerdAxe (single-ASIC, uses same AxeOS HTTP API as nmaxe)
    for m in MINERS.get("nerdaxe", []):
        process(m["name"], fetch_nmaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), has_blocks=False, miner_ip=m["ip"], miner_type="nerdaxe")
    # NerdOctaxe Gamma (octa-ASIC, uses same AxeOS HTTP API as nerdqaxe)
    for m in MINERS.get("nerdoctaxe", []):
        process(m["name"], fetch_nerdqaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), has_blocks=False, miner_ip=m["ip"], miner_type="nerdoctaxe")
    # Lucky Miner LV06/LV07/LV08 (AxeOS HTTP API)
    for m in MINERS.get("luckyminer", []):
        process(m["name"], fetch_nmaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 50), has_blocks=False, miner_ip=m["ip"], miner_type="luckyminer")
    # Jingle Miner BTC Solo Pro/Lite (AxeOS HTTP API)
    for m in MINERS.get("jingleminer", []):
        process(m["name"], fetch_nmaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 100), has_blocks=False, miner_ip=m["ip"], miner_type="jingleminer")
    # Zyber 8G/8GP/8S TinyChipHub (AxeOS HTTP API)
    for m in MINERS.get("zyber", []):
        process(m["name"], fetch_nmaxe(m["ip"]), m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 100), has_blocks=False, miner_ip=m["ip"], miner_type="zyber")
    # GekkoScience USB miners (CGMiner API - uses fetch_avalon for basic CGMiner protocol)
    for m in MINERS.get("gekkoscience", []):
        data = fetch_avalon(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 5), has_blocks=False, miner_ip=m["ip"], miner_type="gekkoscience")
    # iPollo V1/V1 Mini/G1 (CGMiner API — may need manual enable; uses LuCI web on port 80 primarily)
    for m in MINERS.get("ipollo", []):
        data = fetch_antminer(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 2000), has_blocks=True, miner_ip=m["ip"], miner_type="ipollo")
    # ePIC BlockMiner (HTTP REST API on port 4028 — NOT CGMiner TCP)
    for m in MINERS.get("epic", []):
        data = fetch_epic(m["ip"], m.get("port", 4028), username=m.get("username", "root"), password=m.get("password", "letmein"))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 3000), has_blocks=True, miner_ip=m["ip"], miner_type="epic")
    # Elphapex DG1/DG Home (Scrypt — CGMiner best-effort; uses LuCI web on port 80 primarily)
    for m in MINERS.get("elphapex", []):
        data = fetch_antminer(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 3000), has_blocks=True, miner_ip=m["ip"], miner_type="elphapex")
    # LuxOS firmware (CGMiner-compatible API on port 4028)
    for m in MINERS.get("luxos", []):
        data = fetch_antminer(m["ip"], m.get("port", 4028))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000, m.get("watts", 3250), has_blocks=True, miner_ip=m["ip"], miner_type="luxos")
    # BraiinsOS/BOS+ (REST API on port 80)
    for m in MINERS.get("braiins", []):
        data = fetch_braiins(m["ip"], username=m.get("username", "root"), password=m.get("password", ""))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 3250), has_blocks=True, miner_ip=m["ip"], miner_type="braiins")
    # Vnish firmware (REST API on port 80 + CGMiner RPC on port 4028)
    for m in MINERS.get("vnish", []):
        data = fetch_vnish(m["ip"], password=m.get("password", "admin"))
        display_name = m["name"]
        if data and display_name == m["ip"] and data.get("worker_name"):
            display_name = data["worker_name"]
        process(display_name, data, m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0), m.get("watts", 3250), has_blocks=True, miner_ip=m["ip"], miner_type="vnish")
    return total/1000, details, temps, status, power, uptimes, mblocks, mpools, mstats, worker_names, fans, chain_data, hw_errors_data

# === NETWORK & EXTERNAL DATA ===
def fetch_network_stats(coin=None):
    """Fetch network statistics for the specified coin.

    Supports both SHA-256d coins (DGB, BTC, BCH, BC2) and Scrypt coins (LTC, DOGE, DGB-SCRYPT, PEP, CAT).

    Args:
        coin: Coin symbol. If None, uses primary coin.

    Returns:
        dict with network_phs, difficulty, and algorithm, or None on error
    """
    try:
        if coin is None:
            coin = get_primary_coin()
        if coin is None:
            return None
        coin = coin.upper()
        algorithm = get_coin_algorithm(coin)
        coin_config = get_coin_by_symbol(coin)

        # ═══════════════════════════════════════════════════════════════════
        # SHA-256d COINS
        # ═══════════════════════════════════════════════════════════════════
        if coin == "DGB":
            # DigiByte SHA-256d - 75 second block time for SHA-256d algo
            # Priority: Pool API (freshest, local) → RPC → External API (may cache/round)

            # Method 1: Pool API (our own stratum — real-time data from daemon)
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 75) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 2: Direct RPC to DGB daemon (same node as DGB-SCRYPT)
            dgb_rpc_port = coin_config.get("rpc_port", 14022) if coin_config else 14022
            rpc_result = _rpc_call("127.0.0.1", dgb_rpc_port, "getmininginfo")
            if rpc_result and "difficulty_sha256d" in rpc_result:
                diff = float(rpc_result["difficulty_sha256d"])
                if diff > 0:
                    logger.debug(f"DGB network stats from direct RPC: diff={format_difficulty(diff)}")
                    return {"network_phs": (diff * (2**32) / 75) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 3: solomining.io (external fallback — may cache/round values)
            try:
                req = urllib.request.Request("https://dgb.solomining.io/sha256/pool.status", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
                with urllib.request.urlopen(req, timeout=15) as resp:
                    d = json.loads(resp.read().decode().strip().split('\n')[0])
                    diff = float(d.get("netdiff", 0))
                    if diff > 0:
                        return {"network_phs": (diff * (2**32) / 75) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            except Exception:
                pass

            logger.warning("DGB network stats unavailable from all sources")
            return None

        elif coin == "BTC":
            # Bitcoin - 600 second block time
            # Priority: Pool API (freshest, local) → RPC → External API

            # Method 1: Pool API (our own stratum — real-time data from daemon)
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 2: Direct RPC to BTC daemon
            btc_rpc_port = coin_config.get("rpc_port", 8332) if coin_config else 8332
            rpc_result = _rpc_call("127.0.0.1", btc_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    logger.debug(f"BTC network stats from direct RPC: diff={format_difficulty(diff)}")
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 3: blockchain.info API (external fallback)
            try:
                req = urllib.request.Request("https://blockchain.info/q/getdifficulty", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
                with urllib.request.urlopen(req, timeout=15) as resp:
                    diff = float(resp.read().decode().strip())
                    if diff > 0:
                        return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            except Exception:
                pass

            logger.warning("BTC network stats unavailable from all sources")
            return None

        elif coin == "BCH":
            # Bitcoin Cash - 600 second block time
            # Try multiple sources: Pool API (freshest) → RPC → blockchair (external fallback)

            # Method 1: Pool API (preferred - real-time from daemon)
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 2: Direct RPC to BCH daemon
            bch_rpc_port = coin_config.get("rpc_port", 8432) if coin_config else 8432
            rpc_result = _rpc_call("127.0.0.1", bch_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    logger.debug(f"BCH network stats from direct RPC: diff={format_difficulty(diff)}")
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 3: blockchair API (external fallback)
            try:
                d = _http("https://api.blockchair.com/bitcoin-cash/stats", timeout=15)
                if d and "data" in d:
                    diff = float(d["data"].get("difficulty", 0))
                    if diff > 0:
                        return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            except Exception:
                pass

            logger.warning("BCH network stats unavailable from all sources")
            return None

        elif coin == "BC2":
            # Bitcoin II - BC2 has 600 second block time like Bitcoin
            # Try multiple sources in order of reliability:
            # 1. Pool API (primary - already connected to daemon)
            # 2. Direct RPC call to local BC2 daemon (fallback)

            # Method 1: Pool API (preferred - no auth needed)
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 2: Direct RPC to BC2 daemon (fallback)
            # BC2 default RPC port is 8339
            bc2_rpc_port = 8339
            rpc_result = _rpc_call("127.0.0.1", bc2_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    logger.debug(f"BC2 network stats from direct RPC: diff={format_difficulty(diff)}")
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 3: Try getblockchaininfo as last resort
            rpc_result = _rpc_call("127.0.0.1", bc2_rpc_port, "getblockchaininfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    logger.debug(f"BC2 network stats from getblockchaininfo: diff={format_difficulty(diff)}")
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            logger.warning("BC2 network stats unavailable from pool API and direct RPC")
            return None

        # ═══════════════════════════════════════════════════════════════════
        # SHA-256d MERGE-MINEABLE COINS (AuxPoW with Bitcoin)
        # ═══════════════════════════════════════════════════════════════════
        elif coin == "NMC":
            # Namecoin - first AuxPoW coin, 600 second block time (same as Bitcoin)
            # Try pool API first, then direct RPC
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            # Fallback to RPC
            nmc_rpc_port = coin_config.get("rpc_port", 8336) if coin_config else 8336
            rpc_result = _rpc_call("127.0.0.1", nmc_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            logger.warning("NMC network stats unavailable")
            return None

        elif coin == "SYS":
            # Syscoin - 60 second block time, AuxPoW with Bitcoin
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 60) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            sys_rpc_port = coin_config.get("rpc_port", 8370) if coin_config else 8370
            rpc_result = _rpc_call("127.0.0.1", sys_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 60) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            logger.warning("SYS network stats unavailable")
            return None

        elif coin == "XMY":
            # Myriad SHA256d algo - 60 second block time per algo (5 algos, 300s total)
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 60) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            xmy_rpc_port = coin_config.get("rpc_port", 10889) if coin_config else 10889
            rpc_result = _rpc_call("127.0.0.1", xmy_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                # XMY returns per-algo difficulty
                diff = float(rpc_result.get("difficulty_sha256d", rpc_result.get("difficulty", 0)) or 0)
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 60) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            logger.warning("XMY network stats unavailable")
            return None

        elif coin == "FBTC":
            # Fractal Bitcoin - 30 second block time, AuxPoW with Bitcoin
            # Uses "Cadence Mining" - 2 permissionless + 1 merged per 3 blocks
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 30) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            # Fallback to RPC - FBTC uses port 8340
            fbtc_rpc_port = coin_config.get("rpc_port", 8340) if coin_config else 8340
            rpc_result = _rpc_call("127.0.0.1", fbtc_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 30) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            logger.warning("FBTC network stats unavailable")
            return None

        # ═══════════════════════════════════════════════════════════════════
        # SCRYPT COINS (note: much lower hashrate scale than SHA-256d)
        # ═══════════════════════════════════════════════════════════════════
        elif coin == "LTC":
            # Litecoin - 150 second block time (2.5 minutes)
            # Try multiple sources: Pool API (freshest) → RPC → blockchair (external fallback)

            # Method 1: Pool API (preferred - real-time from daemon)
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 150) / 1e15, "difficulty": diff, "algorithm": "scrypt"}

            # Method 2: Direct RPC to LTC daemon
            ltc_rpc_port = coin_config.get("rpc_port", 9332) if coin_config else 9332
            rpc_result = _rpc_call("127.0.0.1", ltc_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    logger.debug(f"LTC network stats from direct RPC: diff={format_difficulty(diff)}")
                    return {"network_phs": (diff * (2**32) / 150) / 1e15, "difficulty": diff, "algorithm": "scrypt"}

            # Method 3: blockchair API (external fallback)
            try:
                d = _http("https://api.blockchair.com/litecoin/stats", timeout=15)
                if d and "data" in d:
                    diff = float(d["data"].get("difficulty", 0))
                    if diff > 0:
                        return {"network_phs": (diff * (2**32) / 150) / 1e15, "difficulty": diff, "algorithm": "scrypt"}
            except Exception:
                pass

            logger.warning("LTC network stats unavailable from all sources")
            return None

        elif coin == "DOGE":
            # Dogecoin - 60 second block time (1 minute)
            # Try multiple sources: Pool API (freshest) → RPC → blockchair (external fallback)

            # Method 1: Pool API (preferred - real-time from daemon)
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 60) / 1e15, "difficulty": diff, "algorithm": "scrypt"}

            # Method 2: Direct RPC to DOGE daemon
            doge_rpc_port = coin_config.get("rpc_port", 22555) if coin_config else 22555
            rpc_result = _rpc_call("127.0.0.1", doge_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    logger.debug(f"DOGE network stats from direct RPC: diff={format_difficulty(diff)}")
                    return {"network_phs": (diff * (2**32) / 60) / 1e15, "difficulty": diff, "algorithm": "scrypt"}

            # Method 3: blockchair API (external fallback)
            try:
                d = _http("https://api.blockchair.com/dogecoin/stats", timeout=15)
                if d and "data" in d:
                    diff = float(d["data"].get("difficulty", 0))
                    if diff > 0:
                        return {"network_phs": (diff * (2**32) / 60) / 1e15, "difficulty": diff, "algorithm": "scrypt"}
            except Exception:
                pass

            logger.warning("DOGE network stats unavailable from all sources")
            return None

        elif coin == "DGB-SCRYPT":
            # DigiByte Scrypt algorithm - shares same blockchain as DGB but different algo
            # DGB Scrypt uses 75 second effective block time (15s actual / 5 algos)
            # Try multiple sources: Pool API (freshest) → RPC → solomining.io (external fallback)

            # Method 1: Pool API (preferred - real-time from daemon)
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 75) / 1e15, "difficulty": diff, "algorithm": "scrypt"}

            # Method 2: Direct RPC to DGB daemon (same node, different algo)
            # DGB RPC port is 14022
            dgb_rpc_port = coin_config.get("rpc_port", 14022) if coin_config else 14022
            rpc_result = _rpc_call("127.0.0.1", dgb_rpc_port, "getmininginfo")
            if rpc_result and "difficulty_scrypt" in rpc_result:
                # DGB returns per-algo difficulty
                diff = float(rpc_result["difficulty_scrypt"])
                if diff > 0:
                    logger.debug(f"DGB-SCRYPT network stats from direct RPC: diff={format_difficulty(diff)}")
                    return {"network_phs": (diff * (2**32) / 75) / 1e15, "difficulty": diff, "algorithm": "scrypt"}

            # Method 3: solomining.io (external fallback)
            try:
                req = urllib.request.Request("https://dgb.solomining.io/scrypt/pool.status", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
                with urllib.request.urlopen(req, timeout=15) as resp:
                    d = json.loads(resp.read().decode().strip().split('\n')[0])
                    diff = float(d.get("netdiff", 0))
                    if diff > 0:
                        return {"network_phs": (diff * (2**32) / 75) / 1e15, "difficulty": diff, "algorithm": "scrypt"}
            except Exception:
                pass

            logger.warning("DGB-SCRYPT network stats unavailable from all sources")
            return None

        elif coin == "PEP":
            # PepeCoin - Scrypt coin, 60 second block time
            # Try pool API first, then direct RPC
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 60) / 1e15, "difficulty": diff, "algorithm": "scrypt"}
            # Fallback to RPC
            pep_rpc_port = coin_config.get("rpc_port", 33873) if coin_config else 33873
            rpc_result = _rpc_call("127.0.0.1", pep_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 60) / 1e15, "difficulty": diff, "algorithm": "scrypt"}
            logger.warning("PEP network stats unavailable")
            return None

        elif coin == "CAT":
            # Catcoin - Scrypt coin, 10 minute (600 second) block time
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "scrypt"}
            cat_rpc_port = coin_config.get("rpc_port", 9932) if coin_config else 9932
            rpc_result = _rpc_call("127.0.0.1", cat_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 600) / 1e15, "difficulty": diff, "algorithm": "scrypt"}
            logger.warning("CAT network stats unavailable")
            return None

        elif coin in ("QBX", "QBITX"):
            # Q-BitX - SHA256d, 150 second block time
            # No external API available — use RPC getnetworkhashps (actual block timing)
            # then pool API / formula fallback
            qbx_rpc_port = coin_config.get("rpc_port", 8344) if coin_config else 8344

            # Method 1: getnetworkhashps RPC — uses moving average over recent blocks
            nhps = _rpc_call("127.0.0.1", qbx_rpc_port, "getnetworkhashps")
            if nhps and isinstance(nhps, (int, float)) and nhps > 0:
                # Still need difficulty for ETB/odds calculations
                mining = _rpc_call("127.0.0.1", qbx_rpc_port, "getmininginfo")
                diff = float(mining.get("difficulty", 0)) if mining else 0
                return {"network_phs": nhps / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 2: Pool API + formula fallback
            pool_stats = fetch_pool_stats_by_symbol(coin)
            pool_data = pool_stats.get("poolStats", {}) if pool_stats else {}
            if pool_data.get("networkDifficulty"):
                diff = float(pool_data.get("networkDifficulty", 0))
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 150) / 1e15, "difficulty": diff, "algorithm": "sha256d"}

            # Method 3: getmininginfo RPC + formula fallback
            rpc_result = _rpc_call("127.0.0.1", qbx_rpc_port, "getmininginfo")
            if rpc_result and "difficulty" in rpc_result:
                diff = float(rpc_result["difficulty"])
                if diff > 0:
                    return {"network_phs": (diff * (2**32) / 150) / 1e15, "difficulty": diff, "algorithm": "sha256d"}
            logger.warning("QBX network stats unavailable")
            return None

        else:
            # Unknown coin - cannot fetch stats
            logger.warning(f"Unknown coin {coin}, cannot fetch network stats")
            return None

    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, json.JSONDecodeError, OSError, ValueError, KeyError) as e:
        return None

def fetch_merge_mining_stats(parent_coin):
    """Fetch network stats for all auxiliary chains when mining a parent chain.

    When merge mining, the same hashrate contributes to finding blocks on ALL
    chains (parent + auxiliary). This function fetches stats for all enabled
    aux chains to calculate combined block odds.

    Args:
        parent_coin: The parent chain symbol (e.g., "BTC", "LTC")

    Returns:
        dict with:
            - parent: {symbol, difficulty, network_phs, algorithm}
            - aux_chains: [{symbol, difficulty, network_phs, algorithm}, ...]
            - combined_daily_odds: sum of all daily odds percentages
            - total_chains: number of chains being mined
    """
    if not parent_coin:
        return None

    parent_coin = parent_coin.upper()
    result = {
        "parent": None,
        "aux_chains": [],
        "combined_daily_odds": 0,
        "total_chains": 0
    }

    # Fetch parent chain stats
    parent_stats = fetch_network_stats(parent_coin)
    if parent_stats:
        result["parent"] = {
            "symbol": parent_coin,
            "difficulty": parent_stats.get("difficulty", 0),
            "network_phs": parent_stats.get("network_phs", 0),
            "algorithm": parent_stats.get("algorithm", "sha256d")
        }
        result["total_chains"] = 1

    # Get enabled auxiliary chains
    enabled_aux = get_enabled_aux_chains(parent_coin)
    if not enabled_aux:
        return result

    # Fetch stats for each enabled aux chain
    for aux_symbol in enabled_aux:
        aux_stats = fetch_network_stats(aux_symbol)
        if aux_stats:
            result["aux_chains"].append({
                "symbol": aux_symbol,
                "difficulty": aux_stats.get("difficulty", 0),
                "network_phs": aux_stats.get("network_phs", 0),
                "algorithm": aux_stats.get("algorithm", "sha256d")
            })
            result["total_chains"] += 1

    return result

def calculate_merge_mining_odds(fleet_ths, merge_stats):
    """Calculate combined block odds across all merge-mined chains.

    When merge mining, the SAME hashrate contributes to block finding on ALL chains.
    This is NOT the same as having separate miners on each chain - it's truly
    simultaneous mining where one share can win blocks on multiple chains.

    Args:
        fleet_ths: Fleet hashrate in TH/s
        merge_stats: Result from fetch_merge_mining_stats()

    Returns:
        dict with per-chain and combined odds
    """
    if not merge_stats or not fleet_ths or fleet_ths <= 0:
        return None

    result = {
        "chains": [],
        "combined_daily_pct": 0,
        "combined_weekly_pct": 0,
        "best_chain": None,
        "best_daily_pct": 0
    }

    fleet_phs = fleet_ths / 1000  # Convert TH/s to PH/s

    # Calculate odds for parent chain
    if merge_stats.get("parent"):
        parent = merge_stats["parent"]
        net_phs = parent.get("network_phs", 0)
        if net_phs > 0:
            share_pct = (fleet_phs / net_phs) * 100
            daily_pct = share_pct * 144  # ~144 blocks/day for BTC-like
            weekly_pct = min(daily_pct * 7, 100)
            chain_odds = {
                "symbol": parent["symbol"],
                "share_pct": share_pct,
                "daily_pct": daily_pct,
                "weekly_pct": weekly_pct,
                "difficulty": parent.get("difficulty", 0),
                "is_parent": True
            }
            result["chains"].append(chain_odds)
            result["combined_daily_pct"] += daily_pct
            if daily_pct > result["best_daily_pct"]:
                result["best_chain"] = parent["symbol"]
                result["best_daily_pct"] = daily_pct

    # Calculate odds for each aux chain
    for aux in merge_stats.get("aux_chains", []):
        net_phs = aux.get("network_phs", 0)
        if net_phs > 0:
            share_pct = (fleet_phs / net_phs) * 100
            # Block times vary by chain - must match coins.manifest.yaml
            blocks_per_day = {
                # SHA-256d aux chains (BTC parent)
                "NMC": 144, "SYS": 1440, "XMY": 1440, "FBTC": 2880,
                # Scrypt aux chains (LTC parent)
                "DOGE": 1440, "PEP": 1440
            }.get(aux["symbol"], 144)
            daily_pct = share_pct * blocks_per_day
            weekly_pct = min(daily_pct * 7, 100)
            chain_odds = {
                "symbol": aux["symbol"],
                "share_pct": share_pct,
                "daily_pct": daily_pct,
                "weekly_pct": weekly_pct,
                "difficulty": aux.get("difficulty", 0),
                "is_parent": False
            }
            result["chains"].append(chain_odds)
            result["combined_daily_pct"] += daily_pct
            if daily_pct > result["best_daily_pct"]:
                result["best_chain"] = aux["symbol"]
                result["best_daily_pct"] = daily_pct

    result["combined_weekly_pct"] = min(result["combined_daily_pct"] * 7, 100)
    return result

def format_merge_mining_odds(merge_odds):
    """Format merge mining odds for display in embeds.

    Args:
        merge_odds: Result from calculate_merge_mining_odds()

    Returns:
        Formatted string for Discord embed
    """
    if not merge_odds or not merge_odds.get("chains"):
        return ""

    lines = []

    # Show per-chain odds
    for chain in merge_odds["chains"]:
        emoji = get_coin_emoji(chain["symbol"])
        chain_type = "⛏️" if chain.get("is_parent") else "🔗"
        lines.append(f"{chain_type} {emoji} **{chain['symbol']}**: `{chain['daily_pct']:.2f}%/day`")

    # Show combined odds
    if len(merge_odds["chains"]) > 1:
        lines.append(f"───────────────")
        lines.append(f"📊 **Combined**: `{merge_odds['combined_daily_pct']:.2f}%/day`")
        if merge_odds.get("best_chain"):
            lines.append(f"🎯 Best odds: {get_coin_emoji(merge_odds['best_chain'])} {merge_odds['best_chain']}")

    return "\n".join(lines)

# ═══════════════════════════════════════════════════════════════════════════════
# PROMETHEUS METRICS & INFRASTRUCTURE HEALTH MONITORING
# ═══════════════════════════════════════════════════════════════════════════════
# Fetches metrics from the Spiral Stratum Go backend for operational visibility.
# This provides insight into share pipeline health, ZMQ block notifications,
# circuit breaker state, backpressure levels, and more.

class InfrastructureHealth:
    """Tracks infrastructure health metrics from Prometheus endpoint."""

    def __init__(self):
        self.last_fetch = 0
        self.metrics = {}
        # Previous values for change detection
        self._prev_circuit_breaker_state = 0
        self._prev_backpressure_level = 0
        self._prev_zmq_health = 2  # 2 = healthy
        self._prev_wal_errors = 0
        self._prev_shares_dropped = 0

    def update(self, metrics_dict):
        """Update with new metrics from Prometheus."""
        self.metrics = metrics_dict
        self.last_fetch = time.time()

    def get_circuit_breaker_state(self):
        """Get circuit breaker state: 0=closed (healthy), 1=open, 2=half-open."""
        return int(self.metrics.get("stratum_circuit_breaker_state", 0))

    def get_circuit_breaker_label(self):
        """Human-readable circuit breaker state."""
        state = self.get_circuit_breaker_state()
        return {0: "🟢 Closed (Healthy)", 1: "🔴 Open (Blocking)", 2: "🟡 Half-Open (Testing)"}[state]

    def get_backpressure_level(self):
        """Get backpressure level: 0=none, 1=warn, 2=critical, 3=emergency."""
        return int(self.metrics.get("stratum_backpressure_level", 0))

    def get_backpressure_label(self):
        """Human-readable backpressure level."""
        level = self.get_backpressure_level()
        return {0: "🟢 None", 1: "🟡 Warning", 2: "🟠 Critical", 3: "🔴 Emergency"}[level]

    def get_backpressure_buffer_fill(self):
        """Get buffer fill percentage (0-100)."""
        return self.metrics.get("stratum_backpressure_buffer_fill_percent", 0)

    def get_zmq_health(self):
        """Get ZMQ health: 0=disabled, 1=connecting, 2=healthy, 3=degraded, 4=failed."""
        return int(self.metrics.get("stratum_zmq_health_status", 0))

    def get_zmq_health_label(self):
        """Human-readable ZMQ health status."""
        health = self.get_zmq_health()
        labels = {
            0: "⚫ Disabled", 1: "🟡 Connecting", 2: "🟢 Healthy",
            3: "🟠 Degraded", 4: "🔴 Failed"
        }
        return labels.get(health, f"❓ Unknown ({health})")

    def get_zmq_connected(self):
        """Check if ZMQ is connected (1=yes, 0=no)."""
        return int(self.metrics.get("stratum_zmq_connected", 0))

    def get_zmq_last_message_age(self):
        """Get seconds since last ZMQ message."""
        return self.metrics.get("stratum_zmq_last_message_age_seconds", 0)

    def get_block_notify_mode(self):
        """Get block notification mode: 1=ZMQ, 0=RPC polling."""
        return int(self.metrics.get("stratum_block_notify_mode", 0))

    def get_block_notify_label(self):
        """Human-readable block notification mode."""
        mode = self.get_block_notify_mode()
        return "🔔 ZMQ (Fast)" if mode == 1 else "🔄 RPC Polling (Slow)"

    def get_wal_errors(self):
        """Get total WAL write + commit errors."""
        write_errors = self.metrics.get("stratum_wal_write_errors_total", 0)
        commit_errors = self.metrics.get("stratum_wal_commit_errors_total", 0)
        return write_errors + commit_errors

    def get_shares_dropped(self):
        """Get total shares lost due to batch drops."""
        return self.metrics.get("stratum_shares_in_dropped_batch_total", 0)

    def get_share_loss_rate(self):
        """Get current share loss rate (0-1)."""
        return self.metrics.get("stratum_share_batch_loss_rate", 0)

    def get_active_connections(self):
        """Get number of active stratum connections."""
        return int(self.metrics.get("stratum_connections_active", 0))

    def get_active_workers(self):
        """Get number of active workers."""
        return int(self.metrics.get("stratum_workers_active", 0))

    def get_vardiff_adjustments(self):
        """Get total vardiff adjustments."""
        return self.metrics.get("stratum_vardiff_adjustments_total", 0)

    def get_best_share_difficulty(self):
        """Get highest share difficulty ever submitted."""
        return self.metrics.get("stratum_best_share_difficulty", 0)

    def get_pool_hashrate_hps(self):
        """Get pool hashrate from metrics (H/s)."""
        return self.metrics.get("stratum_hashrate_pool_hps", 0)

    def get_blocks_found(self):
        """Get total blocks found from metrics."""
        return int(self.metrics.get("stratum_blocks_found_total", 0))

    def get_blocks_orphaned(self):
        """Get total orphaned blocks from metrics."""
        return int(self.metrics.get("stratum_blocks_orphaned_total", 0))

    def check_alerts(self):
        """Check for infrastructure alerts and return list of (alert_type, message, severity)."""
        alerts = []

        # Circuit breaker state change
        cb_state = self.get_circuit_breaker_state()
        if cb_state != self._prev_circuit_breaker_state:
            if cb_state == 1:  # Open
                alerts.append(("circuit_breaker", "Circuit breaker OPENED - database writes blocked!", "critical"))
            elif cb_state == 2:  # Half-open
                alerts.append(("circuit_breaker", "Circuit breaker testing - attempting recovery", "warning"))
            elif cb_state == 0 and self._prev_circuit_breaker_state > 0:
                alerts.append(("circuit_breaker", "Circuit breaker recovered - database writes resumed", "info"))
            self._prev_circuit_breaker_state = cb_state

        # Backpressure level change
        bp_level = self.get_backpressure_level()
        if bp_level >= 2 and self._prev_backpressure_level < 2:
            severity = "critical" if bp_level == 3 else "warning"
            alerts.append(("backpressure", f"Backpressure {self.get_backpressure_label()} - buffer {self.get_backpressure_buffer_fill():.0f}% full", severity))
        elif bp_level < 2 and self._prev_backpressure_level >= 2:
            alerts.append(("backpressure", "Backpressure relieved - share pipeline healthy", "info"))
        self._prev_backpressure_level = bp_level

        # ZMQ health change
        zmq_health = self.get_zmq_health()
        if zmq_health > 2 and self._prev_zmq_health <= 2:
            severity = "critical" if zmq_health == 4 else "warning"
            alerts.append(("zmq_health", f"ZMQ block notification {self.get_zmq_health_label()}", severity))
        elif zmq_health <= 2 and self._prev_zmq_health > 2:
            alerts.append(("zmq_health", "ZMQ block notification recovered", "info"))
        self._prev_zmq_health = zmq_health

        # WAL errors (cumulative, alert on increase)
        wal_errors = self.get_wal_errors()
        if wal_errors > self._prev_wal_errors:
            alerts.append(("wal_errors", f"WAL errors increased to {wal_errors} - shares at risk on crash", "warning"))
        self._prev_wal_errors = wal_errors

        # Share drops (cumulative, alert on increase)
        shares_dropped = self.get_shares_dropped()
        if shares_dropped > self._prev_shares_dropped:
            new_drops = shares_dropped - self._prev_shares_dropped
            alerts.append(("share_loss", f"{new_drops} shares lost due to database issues!", "critical"))
        self._prev_shares_dropped = shares_dropped

        return alerts

    def format_summary(self):
        """Format infrastructure health summary for embed."""
        if not self.metrics:
            return "⚠️ No metrics available"

        lines = [
            f"**Share Pipeline**",
            f"├ Circuit Breaker: {self.get_circuit_breaker_label()}",
            f"├ Backpressure: {self.get_backpressure_label()}",
            f"└ Buffer Fill: `{self.get_backpressure_buffer_fill():.0f}%`",
            f"",
            f"**Block Notifications**",
            f"├ Mode: {self.get_block_notify_label()}",
            f"├ ZMQ Health: {self.get_zmq_health_label()}",
            f"└ Last ZMQ: `{self.get_zmq_last_message_age():.0f}s ago`",
            f"",
            f"**Connections**",
            f"├ Active: `{self.get_active_connections()}`",
            f"└ Workers: `{self.get_active_workers()}`",
        ]
        return "\n".join(lines)


# Global infrastructure health tracker
_infra_health = InfrastructureHealth()


def fetch_prometheus_metrics():
    """Fetch and parse Prometheus metrics from Spiral Stratum backend.

    Returns dict of metric_name -> value for gauge/counter metrics.
    Histogram metrics are skipped (too complex for simple monitoring).
    """
    if not CONFIG.get("metrics_enabled", True):
        return None

    metrics_url = CONFIG.get("metrics_url", "http://localhost:9100/metrics")
    metrics_token = CONFIG.get("metrics_token", "")

    try:
        headers = {}
        if metrics_token:
            headers["Authorization"] = f"Bearer {metrics_token}"

        req = urllib.request.Request(metrics_url, headers=headers)
        with urllib.request.urlopen(req, timeout=10) as resp:
            content = resp.read().decode('utf-8')

        # Parse Prometheus text format
        metrics = {}
        for line in content.split('\n'):
            line = line.strip()
            # Skip comments and empty lines
            if not line or line.startswith('#'):
                continue
            # Skip histogram bucket/sum/count lines
            if '_bucket{' in line or '_sum' in line or '_count' in line:
                continue

            # Parse metric line: metric_name{labels} value or metric_name value
            try:
                # Handle metrics with labels: metric_name{label="value"} 123.45
                if '{' in line:
                    name_part = line.split('{')[0]
                    value_part = line.split('}')[-1].strip()
                else:
                    parts = line.split()
                    if len(parts) >= 2:
                        name_part = parts[0]
                        value_part = parts[1]
                    else:
                        continue

                # Parse value
                try:
                    value = float(value_part)
                    metrics[name_part] = value
                except ValueError:
                    continue

            except Exception:
                continue

        return metrics

    except Exception as e:
        logger.debug(f"Failed to fetch Prometheus metrics: {e}")
        return None


def update_infrastructure_health():
    """Fetch Prometheus metrics and update infrastructure health state."""
    global _infra_health

    metrics = fetch_prometheus_metrics()
    if metrics:
        _infra_health.update(metrics)
        return _infra_health.check_alerts()
    return []


def get_infrastructure_health():
    """Get the global infrastructure health tracker."""
    return _infra_health


def fetch_spiral_router_profiles():
    """Fetch Spiral Router difficulty profiles from pool API.

    Returns dict of miner class -> profile info (min/max diff, target share time).
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = CONFIG.get("pool_id", "dgb_sha256_1")

        # Try the router profiles endpoint
        url = f"{pool_url}/api/pools/{pool_id}/router/profiles"
        data = _http(url)
        if data:
            return data

        # Fallback: try without pool_id
        url = f"{pool_url}/api/router/profiles"
        return _http(url)

    except Exception as e:
        logger.debug(f"Failed to fetch Spiral Router profiles: {e}")
        return None


def fetch_worker_class_counts():
    """Fetch worker counts by miner class from pool API.

    Returns dict of class_name -> worker count.
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = CONFIG.get("pool_id", "dgb_sha256_1")

        url = f"{pool_url}/api/pools/{pool_id}/workers-by-class"
        data = _http(url)
        if data:
            return data

        return None

    except Exception as e:
        logger.debug(f"Failed to fetch worker class counts: {e}")
        return None


def fetch_ha_status():
    """Fetch High Availability cluster status from pool API.

    Returns dict with VIP status, DB failover status, pool failover status.
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        api_key = CONFIG.get("pool_admin_api_key", "")

        headers = {}
        if api_key:
            headers["X-API-Key"] = api_key

        # Fetch HA status
        url = f"{pool_url}/api/ha/status"
        req = urllib.request.Request(url, headers=headers)
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode('utf-8'))

    except Exception as e:
        logger.debug(f"Failed to fetch HA status: {e}")
        return None


def fetch_share_pipeline_stats():
    """Fetch share pipeline statistics from pool API.

    Returns detailed pipeline stats including batch sizes, latencies, etc.
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        pool_id = CONFIG.get("pool_id", "dgb_sha256_1")

        url = f"{pool_url}/api/pools/{pool_id}/pipeline/stats"
        return _http(url)

    except Exception as e:
        logger.debug(f"Failed to fetch pipeline stats: {e}")
        return None


def create_infrastructure_health_embed(infra_health, router_profiles=None, ha_status=None):
    """Create an embed showing infrastructure health status.

    Args:
        infra_health: InfrastructureHealth instance
        router_profiles: Optional Spiral Router profile data
        ha_status: Optional HA cluster status data

    Returns:
        Embed dict for Discord
    """
    fields = []

    # Share pipeline health
    if infra_health.metrics:
        fields.append({
            "name": "🔧 Share Pipeline",
            "value": (
                f"Circuit Breaker: {infra_health.get_circuit_breaker_label()}\n"
                f"Backpressure: {infra_health.get_backpressure_label()}\n"
                f"Buffer: `{infra_health.get_backpressure_buffer_fill():.0f}%`\n"
                f"Loss Rate: `{infra_health.get_share_loss_rate()*100:.3f}%`"
            ),
            "inline": True
        })

        # ZMQ/Block notifications
        fields.append({
            "name": "🔔 Block Notifications",
            "value": (
                f"Mode: {infra_health.get_block_notify_label()}\n"
                f"ZMQ: {infra_health.get_zmq_health_label()}\n"
                f"Last Msg: `{infra_health.get_zmq_last_message_age():.0f}s ago`"
            ),
            "inline": True
        })

        # Connection stats
        fields.append({
            "name": "🔌 Connections",
            "value": (
                f"Active: `{infra_health.get_active_connections()}`\n"
                f"Workers: `{infra_health.get_active_workers()}`\n"
                f"Vardiff: `{infra_health.get_vardiff_adjustments():.0f}` adj"
            ),
            "inline": True
        })

    # Spiral Router profiles
    if router_profiles:
        profile_lines = []
        for profile in router_profiles.get("profiles", [])[:6]:  # Show top 6
            name = profile.get("name", "Unknown")
            min_diff = profile.get("min_difficulty", 0)
            max_diff = profile.get("max_difficulty", 0)
            profile_lines.append(f"`{name}`: {min_diff}-{max_diff}")
        if profile_lines:
            fields.append({
                "name": "🎯 Spiral Router Profiles",
                "value": "\n".join(profile_lines),
                "inline": False
            })

    # HA Status
    if ha_status:
        vip_status = ha_status.get("vip", {})
        db_status = ha_status.get("database", {})

        vip_owner = vip_status.get("owner", "unknown")
        vip_ip = vip_status.get("address", "N/A")
        db_primary = db_status.get("primary", "unknown")
        db_healthy = "🟢" if db_status.get("healthy", False) else "🔴"

        fields.append({
            "name": "🏥 High Availability",
            "value": (
                f"VIP: `{vip_ip}` → {vip_owner}\n"
                f"DB Primary: {db_healthy} {db_primary}\n"
                f"Replicas: `{db_status.get('replica_count', 0)}`"
            ),
            "inline": True
        })

    # Determine overall health color
    cb_state = infra_health.get_circuit_breaker_state() if infra_health.metrics else 0
    bp_level = infra_health.get_backpressure_level() if infra_health.metrics else 0
    zmq_health = infra_health.get_zmq_health() if infra_health.metrics else 2

    if cb_state > 0 or bp_level >= 3 or zmq_health >= 4:
        color = COLORS["red"]
        status = "🔴 CRITICAL"
    elif bp_level >= 2 or zmq_health >= 3:
        color = COLORS["yellow"]
        status = "🟡 WARNING"
    else:
        color = COLORS["green"]
        status = "🟢 HEALTHY"

    return _embed(
        theme("infra.health.title", status=status),
        f"{theme('infra.health.body')}\n`{local_now().strftime('%Y-%m-%d %H:%M')}`",
        color,
        fields,
        footer=theme("infra.health.footer")
    )


def create_infrastructure_alert_embed(alert_type, message, severity):
    """Create an alert embed for infrastructure issues.

    Args:
        alert_type: Type of alert (circuit_breaker, backpressure, zmq_health, etc.)
        message: Alert message
        severity: critical, warning, or info

    Returns:
        Embed dict for Discord
    """
    icons = {
        "circuit_breaker": "⚡",
        "backpressure": "📊",
        "zmq_health": "🔔",
        "wal_errors": "💾",
        "share_loss": "⚠️",
    }
    colors = {
        "critical": COLORS["red"],
        "warning": COLORS["yellow"],
        "info": COLORS["green"],
    }

    icon = icons.get(alert_type, "🔧")
    color = colors.get(severity, COLORS["blue"])
    title_prefix = "🚨" if severity == "critical" else "⚠️" if severity == "warning" else "ℹ️"

    return _embed(
        theme("infra.alert.title", prefix=title_prefix, icon=icon),
        message,
        color,
        [],
        footer=theme("infra.alert.footer", alert_type=alert_type, severity=severity)
    )


def create_api_sentinel_alert_embed(alert):
    """Create a Discord embed for an alert from the Go API Sentinel.

    Args:
        alert: dict with keys: alert_type, severity, coin, pool_id, message, timestamp

    Returns:
        Embed dict for Discord
    """
    severity = alert.get("severity", "warning")
    alert_type = alert.get("alert_type", "unknown")
    coin = alert.get("coin", "")
    message = alert.get("message", "")

    colors = {
        "critical": COLORS["red"],
        "warning": COLORS["yellow"],
        "info": COLORS["blue"],
    }

    # Map alert types to user-friendly titles
    title_map = {
        "wal_stuck_entry": "WAL Entry Stuck",
        "block_drought": "Block Drought",
        "share_db_critical": "Share DB Critical",
        "share_db_degraded": "Share DB Degraded",
        "share_batch_dropped": "Share Batches Dropped",
        "circuit_breaker_open": "Circuit Breaker Open",
        "circuit_breaker_halfopen": "Circuit Breaker Half-Open",
        "all_nodes_down": "All Daemon Nodes Down",
        "chain_tip_stall": "Chain Tip Stalled",
        "daemon_no_peers": "Daemon Zero Peers",
        "daemon_low_peers": "Daemon Low Peers",
        "wal_recovery_stuck": "WAL Recovery Stuck",
        "miner_disconnect_spike": "Miner Disconnect Spike",
        "hashrate_drop": "Pool Hashrate Drop",
        "backpressure_emergency": "Buffer Emergency",
        "backpressure_critical": "Buffer Critical",
        "backpressure_warn": "Buffer Warning",
        "zmq_failed": "ZMQ Failed",
        "zmq_degraded": "ZMQ Degraded",
        "node_health_low": "Node Health Low",
        "wal_disk_space_low": "WAL Disk Space Low",
        "wal_file_count_high": "WAL File Count High",
        "false_rejection_rate": "Block False Rejection Rate",
        "retry_storm": "Block Submit Retry Storm",
        "payment_processor_stalled": "Payment Processor Stalled",
        "db_failover": "Database Failover",
        "ha_flapping": "HA Role Flapping",
        "orphan_rate_high": "High Orphan Rate",
        "block_maturity_stall": "Block Maturity Stall",
        "goroutine_limit": "Goroutine Limit",
        "goroutine_growth": "Goroutine Growth",
    }

    title_prefix = "\U0001f6a8" if severity == "critical" else "\u26a0\ufe0f" if severity == "warning" else "\u2139\ufe0f"
    friendly_title = title_map.get(alert_type, alert_type.replace("_", " ").title())
    coin_label = f" [{coin}]" if coin else ""

    return _embed(
        theme("infra.api.title", prefix=title_prefix, title=friendly_title, coin=coin_label),
        message,
        colors.get(severity, COLORS["yellow"]),
        [],
        footer=theme("infra.api.footer", alert_type=f"{alert_type} | {severity}")
    )


# Alert types that the Python Sentinel already monitors natively via Prometheus metrics.
# These are skipped when polling /api/sentinel/alerts to avoid duplicate notifications.
_NATIVE_PYTHON_ALERT_TYPES = {
    "backpressure_emergency", "backpressure_critical", "backpressure_warn",
    "circuit_breaker_open", "circuit_breaker_halfopen",
    "zmq_failed", "zmq_degraded",
    "orphan_rate_high",
}

# Track the last poll time for API Sentinel alerts
_api_sentinel_last_poll = 0.0


def check_api_sentinel_alerts(state):
    """Poll the Go API Sentinel for recent alerts and send notifications.

    The Go API Sentinel detects 27 critical infrastructure alerts that the
    Python Sentinel has zero visibility into (WAL, share DB, payment stalls,
    daemon peers, etc.). This function bridges those alerts to Discord/Telegram.

    Alerts that Python already monitors natively (backpressure, circuit breaker,
    ZMQ, orphan rate) are skipped to avoid duplicate notifications.

    Args:
        state: MonitorState instance

    Returns:
        int: Number of alerts forwarded
    """
    global _api_sentinel_last_poll

    pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
    current_time = time.time()

    # Build the since parameter from last poll time
    if _api_sentinel_last_poll > 0:
        from datetime import datetime, timezone
        since_dt = datetime.fromtimestamp(_api_sentinel_last_poll, tz=timezone.utc)
        since_param = f"?since={since_dt.strftime('%Y-%m-%dT%H:%M:%SZ')}"
    else:
        # First poll: get last 5 minutes
        since_param = ""

    _api_sentinel_last_poll = current_time

    try:
        url = f"{pool_url}/api/sentinel/alerts{since_param}"
        req = urllib.request.Request(url, method='GET')
        req.add_header('User-Agent', 'SpiralSentinel/1.0')
        with urllib.request.urlopen(req, timeout=10) as resp:
            alerts = json.loads(resp.read().decode('utf-8'))
    except Exception as e:
        logger.debug(f"API Sentinel poll failed: {e}")
        return 0

    if not isinstance(alerts, list) or len(alerts) == 0:
        return 0

    forwarded = 0
    for alert in alerts:
        alert_type = alert.get("alert_type", "")

        # Skip alert types that Python already monitors natively
        if alert_type in _NATIVE_PYTHON_ALERT_TYPES:
            continue

        # Create prefixed alert key for cooldown tracking
        # No additional cooldown — Go API Sentinel already applies 15min cooldown
        alert_key = f"pool_{alert_type}"

        embed = create_api_sentinel_alert_embed(alert)
        send_alert(alert_key, embed, state)
        forwarded += 1

        coin = alert.get("coin", "")
        severity = alert.get("severity", "")
        logger.info(f"API Sentinel alert forwarded: {alert_type} [{severity}] {coin}")

    if forwarded > 0:
        logger.info(f"Forwarded {forwarded} API Sentinel alert(s) to notifications")

    return forwarded


def fetch_dgb_price():
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=digibyte,bitcoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            dgb, btc = d.get("digibyte", {}), d.get("bitcoin", {})
            dgb_btc = (dgb.get("usd", 0) / btc.get("usd", 1)) if btc.get("usd", 0) > 0 else 0
            result = {c: dgb.get(c, 0) for c in CURRENCY_CODES}
            result["sats"] = int(dgb_btc * 1e8)
            return result
        return None
    return _cached_fetch("dgb_price", _fetch, 120)

def fetch_btc_price():
    """Fetch Bitcoin price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            btc = d.get("bitcoin", {})
            return {c: btc.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("btc_price", _fetch, 120)

def fetch_bch_price():
    """Fetch Bitcoin Cash price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=bitcoin-cash&vs_currencies=" + VS_CURRENCIES)
        if d:
            bch = d.get("bitcoin-cash", {})
            return {c: bch.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("bch_price", _fetch, 120)

def fetch_all_prices():
    """Fetch all coin prices in a single API call for efficiency.

    Includes both SHA-256d coins (DGB, BTC, BCH, BC2) and Scrypt coins (LTC, DOGE, PEP, CAT).
    BC2 may not be listed on CoinGecko yet.
    Also includes sat values (price in BTC satoshis) for all non-BTC coins.
    """
    def _fetch():
        # Include all supported coins in a single API call
        coins = "digibyte,bitcoin,bitcoin-cash,litecoin,dogecoin,namecoin,syscoin,myriadcoin,fractal-bitcoin,pepecoin,catcoin"
        d = _http(f"https://api.coingecko.com/api/v3/simple/price?ids={coins}&vs_currencies=" + VS_CURRENCIES)
        if d:
            # Map CoinGecko IDs to internal symbols
            gecko_map = {
                "digibyte": "dgb", "bitcoin": "btc", "bitcoin-cash": "bch",
                "litecoin": "ltc", "dogecoin": "doge", "namecoin": "nmc",
                "syscoin": "sys", "myriadcoin": "xmy", "fractal-bitcoin": "fbtc",
                "pepecoin": "pep", "catcoin": "cat"
            }

            _cur = get_currency_meta().get("code", "usd")
            btc_fiat = d.get("bitcoin", {}).get(_cur, 0) or d.get("bitcoin", {}).get("usd", 1) or 1

            def to_sats(coin_fiat):
                return int((coin_fiat / btc_fiat) * 1e8) if coin_fiat > 0 else 0

            result = {}
            for gecko_id, symbol in gecko_map.items():
                coin_data = d.get(gecko_id, {})
                # Add all currency prices: {symbol}_{currency_code}
                for cur_code in CURRENCY_CODES:
                    result[f"{symbol}_{cur_code}"] = coin_data.get(cur_code, 0)
                # Add sats for all non-BTC coins
                if symbol != "btc":
                    result[f"{symbol}_sats"] = to_sats(coin_data.get(_cur, 0) or coin_data.get("usd", 0))

            # DGB-SCRYPT uses DGB prices (same blockchain, different algo)
            for cur_code in CURRENCY_CODES:
                result[f"dgb-scrypt_{cur_code}"] = result.get(f"dgb_{cur_code}", 0)
            result["dgb-scrypt_sats"] = result.get("dgb_sats", 0)

            return result
        return None
    return _cached_fetch("all_prices", _fetch, 120)

def fetch_ltc_price():
    """Fetch Litecoin price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=litecoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            ltc = d.get("litecoin", {})
            return {c: ltc.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("ltc_price", _fetch, 120)

def fetch_doge_price():
    """Fetch Dogecoin price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=dogecoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            doge = d.get("dogecoin", {})
            return {c: doge.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("doge_price", _fetch, 120)

def fetch_nmc_price():
    """Fetch Namecoin price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=namecoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            nmc = d.get("namecoin", {})
            return {c: nmc.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("nmc_price", _fetch, 120)

def fetch_sys_price():
    """Fetch Syscoin price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=syscoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            sys_coin = d.get("syscoin", {})
            return {c: sys_coin.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("sys_price", _fetch, 120)

def fetch_xmy_price():
    """Fetch Myriad price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=myriadcoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            xmy = d.get("myriadcoin", {})
            return {c: xmy.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("xmy_price", _fetch, 120)

def fetch_fbtc_price():
    """Fetch Fractal Bitcoin price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=fractal-bitcoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            fbtc = d.get("fractal-bitcoin", {})
            return {c: fbtc.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("fbtc_price", _fetch, 120)

def fetch_pep_price():
    """Fetch PepeCoin price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=pepecoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            pep = d.get("pepecoin", {})
            return {c: pep.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("pep_price", _fetch, 120)

def fetch_cat_price():
    """Fetch Catcoin price from CoinGecko."""
    def _fetch():
        d = _http("https://api.coingecko.com/api/v3/simple/price?ids=catcoin&vs_currencies=" + VS_CURRENCIES)
        if d:
            cat = d.get("catcoin", {})
            return {c: cat.get(c, 0) for c in CURRENCY_CODES}
        return None
    return _cached_fetch("cat_price", _fetch, 120)

def fetch_coin_price(symbol):
    """Fetch price for a specific coin.

    Supports all coins: SHA-256d (DGB, BTC, BCH, BC2, NMC, SYS, XMY)
    and Scrypt (LTC, DOGE, DGB-SCRYPT, PEP, CAT).
    DGB-SCRYPT uses DGB price since it's the same blockchain.
    """
    symbol = symbol.upper()
    # SHA-256d coins
    if symbol == "DGB":
        return fetch_dgb_price()
    elif symbol == "BTC":
        return fetch_btc_price()
    elif symbol == "BCH":
        return fetch_bch_price()
    elif symbol == "BC2":
        # BC2 is a new coin - may not be listed on price APIs yet
        return None
    # SHA-256d merge-mineable coins
    elif symbol == "NMC":
        return fetch_nmc_price()
    elif symbol == "SYS":
        return fetch_sys_price()
    elif symbol == "XMY":
        return fetch_xmy_price()
    # Scrypt coins
    elif symbol == "LTC":
        return fetch_ltc_price()
    elif symbol == "DOGE":
        return fetch_doge_price()
    elif symbol == "FBTC":
        return fetch_fbtc_price()
    # Scrypt merge-mineable coins
    elif symbol == "PEP":
        return fetch_pep_price()
    elif symbol == "CAT":
        return fetch_cat_price()
    elif symbol == "DGB-SCRYPT":
        # DGB-SCRYPT mines DGB - same coin, different algorithm
        return fetch_dgb_price()
    return None

def get_coin_volatility_threshold(symbol):
    """Get the appropriate volatility threshold for trend detection per coin.

    Different coins have different network volatility characteristics.
    Scrypt coins generally have more stable hashrate than SHA-256d due to
    less profit-switching between algorithms.

    Returns 5 (moderate) as default if symbol is None or unknown.
    """
    if not symbol:
        return 5  # Default moderate threshold
    thresholds = {
        # SHA-256d coins
        "DGB": 5,   # DigiByte: high volatility due to multi-algo profit switching
        "BTC": 2,   # Bitcoin: stable network, low volatility
        "BCH": 3,   # Bitcoin Cash: moderate volatility
        "BC2": 10,  # Bitcoin II: new chain, highly volatile as network grows
        # SHA-256d merge-mineable coins
        "NMC": 3,   # Namecoin: stable due to merge-mining with Bitcoin
        "SYS": 4,   # Syscoin: relatively stable merge-mined chain
        "XMY": 6,   # Myriad: multi-algo, moderate volatility
        "FBTC": 8,  # Fractal Bitcoin: newer chain, higher volatility
        "QBX": 10,  # Q-BitX: new chain, high volatility
        # Scrypt coins (generally more stable due to merge-mining)
        "LTC": 3,   # Litecoin: relatively stable, some merge-mining with DOGE
        "DOGE": 3,  # Dogecoin: stable due to merge-mining with LTC
        "DGB-SCRYPT": 6,  # DGB Scrypt algo: moderate volatility
        "PEP": 8,   # PepeCoin: smaller network, higher volatility
        "CAT": 10,  # Catcoin: small network, high volatility
    }
    return thresholds.get(symbol.upper(), 5)

def get_coin_volatility_description(symbol):
    """Get description of network volatility for a coin (used in documentation/reports)."""
    descriptions = {
        # SHA-256d coins
        "DGB": "DigiByte's SHA256 hashrate is volatile (20-50% swings are common due to profit-switching miners)",
        "BTC": "Bitcoin's network hashrate is relatively stable with gradual changes",
        "BCH": "Bitcoin Cash network has moderate volatility from profit-switching miners",
        "BC2": "Bitcoin II is a new chain (Dec 2024) with high volatility as the network grows",
        # SHA-256d merge-mineable coins
        "NMC": "Namecoin's network is stabilized by merge-mining with Bitcoin (first AuxPoW coin)",
        "SYS": "Syscoin network is stabilized by merge-mining with Bitcoin",
        "XMY": "Myriad is a multi-algo chain with moderate hashrate volatility per algorithm",
        "FBTC": "Fractal Bitcoin is a newer merge-mineable chain with higher volatility as the network grows",
        # Scrypt coins
        "LTC": "Litecoin's network hashrate is relatively stable due to merge-mining with Dogecoin",
        "DOGE": "Dogecoin's network is stabilized by merge-mining with Litecoin",
        "DGB-SCRYPT": "DigiByte Scrypt algo has moderate volatility from multi-algo dynamics",
        "PEP": "PepeCoin has moderate volatility as a merge-mineable Scrypt coin",
        "CAT": "Catcoin is a standalone Scrypt chain with moderate hashrate volatility",
    }
    return descriptions.get(symbol.upper(), "Network hashrate")

def fetch_block_reward():
    """
    Fetch block reward for the currently active coin (legacy function for backwards compatibility).
    DEPRECATED: Use fetch_block_reward_for_coin(coin_symbol) instead with explicit coin.
    """
    # Try to detect active coin from environment or config
    try:
        import os
        active_coin = os.getenv("ACTIVE_COIN")
        if not active_coin:
            # Try to get first enabled coin from config
            primary = get_primary_coin()
            if primary:
                active_coin = primary
            else:
                logger.error("No coins configured and ACTIVE_COIN not set")
                return None
        return fetch_block_reward_for_coin(active_coin)
    except Exception as e:
        logger.error(f"Could not determine active coin for block reward: {e}")
        return None

def _get_block_height_from_pool(coin_symbol):
    """Get block height from pool API for a coin.

    This is the authoritative source for block height when mining a coin,
    as it comes directly from the pool's connected daemon.

    Returns: block height (int) or 0 if unavailable
    """
    try:
        coin_config = get_coin_by_symbol(coin_symbol)
        if coin_config:
            pool_stats = fetch_pool_stats_for_coin(coin_config)
            if pool_stats:
                block_height = pool_stats.get("poolStats", {}).get("blockHeight", 0)
                if block_height and block_height > 0:
                    return int(block_height)
    except (KeyError, TypeError, ValueError):
        pass
    return 0

def _get_block_height_from_rpc(coin_symbol, default_port):
    """Get block height from direct RPC call to daemon.

    Fallback when pool API is unavailable.

    Returns: block height (int) or 0 if unavailable
    """
    try:
        coin_config = get_coin_by_symbol(coin_symbol)
        rpc_port = coin_config.get("rpc_port", default_port) if coin_config else default_port

        # Try getblockchaininfo first (more reliable)
        rpc_result = _rpc_call("127.0.0.1", rpc_port, "getblockchaininfo")
        if rpc_result and "blocks" in rpc_result:
            return int(rpc_result["blocks"])

        # Fallback to getblockcount
        rpc_result = _rpc_call("127.0.0.1", rpc_port, "getblockcount")
        if rpc_result:
            return int(rpc_result)
    except (KeyError, TypeError, ValueError):
        pass
    return 0

def fetch_block_reward_for_coin(symbol):
    """Fetch block reward for a specific coin (DGB, BTC, or BCH)."""
    symbol = symbol.upper()

    def _fetch_dgb():
        bh = 22600000
        try:
            req = urllib.request.Request("https://chainz.cryptoid.info/dgb/api.dws?q=getblockcount", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
            with urllib.request.urlopen(req, timeout=10) as resp: bh = int(resp.read().decode().strip())
        except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError, ValueError):
            pass  # Use default block height
        sr = 2157 / 2
        if bh > 1430000:
            for _ in range(int((bh - 1430000) * 15 / (3600 * 24 * 365 / 12))): sr = sr * 98884 / 100000
        return {"block_height": bh, "sha256_reward": sr, "symbol": "DGB"}

    def _fetch_btc():
        # BTC block reward: 3.125 BTC after April 2024 halving (block 840,000)
        bh = 870000  # Default estimate
        try:
            # Use blockchain.info API for block height
            data = _http("https://blockchain.info/q/getblockcount")
            if data: bh = int(data)
        except (ValueError, TypeError):
            pass
        # Calculate reward based on halvings (every 210,000 blocks)
        halvings = bh // 210000
        reward = 50 / (2 ** halvings)  # Initial 50 BTC, halved each cycle
        return {"block_height": bh, "sha256_reward": reward, "symbol": "BTC"}

    def _fetch_bch():
        # BCH block reward: Same halving schedule as BTC (3.125 BCH after halving)
        bh = 870000  # Default estimate
        try:
            # Use blockchair API for BCH
            data = _http("https://api.blockchair.com/bitcoin-cash/stats")
            if data and data.get("data", {}).get("blocks"):
                bh = data["data"]["blocks"]
        except (ValueError, TypeError, KeyError):
            pass
        # Calculate reward based on halvings (every 210,000 blocks)
        halvings = bh // 210000
        reward = 50 / (2 ** halvings)  # Initial 50 BCH, halved each cycle
        return {"block_height": bh, "sha256_reward": reward, "symbol": "BCH"}

    def _fetch_bc2():
        # BC2 (Bitcoin II): New chain started December 2024
        # Same halving schedule as Bitcoin: 50 BC2 initial, halving every 210,000 blocks
        bh = 1000  # Default fallback (new chain)

        # Method 1: Pool API (authoritative - connected to daemon)
        pool_bh = _get_block_height_from_pool("BC2")
        if pool_bh > 0:
            bh = pool_bh
        else:
            # Method 2: Direct RPC to BC2 daemon (fallback)
            rpc_bh = _get_block_height_from_rpc("BC2", 8339)
            if rpc_bh > 0:
                bh = rpc_bh

        # Calculate reward based on halvings (every 210,000 blocks)
        halvings = bh // 210000
        reward = 50 / (2 ** halvings)  # Initial 50 BC2, halved each cycle
        return {"block_height": bh, "sha256_reward": reward, "symbol": "BC2"}

    def _fetch_qbx():
        # Q-BitX (QBX): Post-Quantum Bitcoin fork, SHA-256d
        # 12.5 QBX initial reward, halving every 840,000 blocks (~4 years at 150s blocks)
        # 2.5 min block time, 21M max supply
        bh = 1000  # Default fallback (new chain, launched April 2025)

        # Method 1: Pool API (if mining QBX)
        pool_bh = _get_block_height_from_pool("QBX")
        if pool_bh > 0:
            bh = pool_bh
        else:
            # Method 2: Direct RPC to QBX daemon (port 8344)
            rpc_bh = _get_block_height_from_rpc("QBX", 8344)
            if rpc_bh > 0:
                bh = rpc_bh

        # Calculate reward based on halvings (every 840,000 blocks)
        halvings = bh // 840000
        reward = 12.5 / (2 ** min(halvings, 10))  # Initial 12.5 QBX, halved each cycle
        return {"block_height": bh, "sha256_reward": reward, "symbol": "QBX"}

    # === SHA-256d MERGE-MINEABLE COINS ===

    def _fetch_nmc():
        # Namecoin (NMC): First AuxPoW coin, same halving schedule as Bitcoin
        # 50 NMC initial, halving every 210,000 blocks
        bh = 700000  # Default fallback

        # Method 1: Pool API (if mining NMC)
        pool_bh = _get_block_height_from_pool("NMC")
        if pool_bh > 0:
            bh = pool_bh
        else:
            # Method 2: Direct RPC to Namecoin daemon (default port 8336)
            rpc_bh = _get_block_height_from_rpc("NMC", 8336)
            if rpc_bh > 0:
                bh = rpc_bh

        halvings = bh // 210000
        reward = 50 / (2 ** halvings)
        return {"block_height": bh, "sha256_reward": reward, "symbol": "NMC"}

    def _fetch_sys():
        # Syscoin (SYS): AuxPoW with Bitcoin, different reward schedule
        # Current block reward approximately 1.25 SYS (after multiple halvings)
        bh = 1500000  # Default fallback

        # Method 1: Pool API (if mining SYS)
        pool_bh = _get_block_height_from_pool("SYS")
        if pool_bh > 0:
            bh = pool_bh
        else:
            # Method 2: Direct RPC to Syscoin daemon (default port 8370)
            rpc_bh = _get_block_height_from_rpc("SYS", 8370)
            if rpc_bh > 0:
                bh = rpc_bh

        # Syscoin has complex reward schedule, using approximate current value
        reward = 1.25  # Approximate current block reward
        return {"block_height": bh, "sha256_reward": reward, "symbol": "SYS"}

    def _fetch_xmy():
        # Myriad (XMY): Multi-algo coin, AuxPoW on SHA256d algo
        # Block reward approximately 500 XMY per block (decreases over time)
        bh = 4000000  # Default fallback

        # Method 1: Pool API (if mining XMY)
        pool_bh = _get_block_height_from_pool("XMY")
        if pool_bh > 0:
            bh = pool_bh
        else:
            # Method 2: Direct RPC to Myriad daemon (default port 10889)
            rpc_bh = _get_block_height_from_rpc("XMY", 10889)
            if rpc_bh > 0:
                bh = rpc_bh

        # XMY has decreasing reward schedule
        reward = 500  # Approximate current block reward
        return {"block_height": bh, "sha256_reward": reward, "symbol": "XMY"}

    # === SCRYPT COINS ===

    def _fetch_ltc():
        # LTC block reward: 6.25 LTC after August 2023 halving (block 2,520,000)
        # Halving every 840,000 blocks
        bh = 2700000  # Default estimate
        try:
            data = _http("https://api.blockchair.com/litecoin/stats")
            if data and data.get("data", {}).get("blocks"):
                bh = data["data"]["blocks"]
        except (ValueError, TypeError, KeyError):
            pass
        halvings = bh // 840000
        reward = 50 / (2 ** halvings)  # Initial 50 LTC, halved each cycle
        return {"block_height": bh, "scrypt_reward": reward, "symbol": "LTC"}

    def _fetch_doge():
        # DOGE block reward: Fixed 10,000 DOGE per block (since block 600,000)
        bh = 5500000  # Default estimate
        try:
            data = _http("https://api.blockchair.com/dogecoin/stats")
            if data and data.get("data", {}).get("blocks"):
                bh = data["data"]["blocks"]
        except (ValueError, TypeError, KeyError):
            pass
        reward = 10000  # Fixed 10,000 DOGE since block 600,000
        return {"block_height": bh, "scrypt_reward": reward, "symbol": "DOGE"}

    def _fetch_pep():
        # PepeCoin (PEP): 50 PEP initial reward, halving every 100,000 blocks
        # Launched 2016, 60-second block time
        bh = 4500000  # Default fallback

        # Method 1: Pool API (if mining PEP)
        pool_bh = _get_block_height_from_pool("PEP")
        if pool_bh > 0:
            bh = pool_bh
        else:
            # Method 2: Direct RPC to PepeCoin daemon (default port 33873)
            rpc_bh = _get_block_height_from_rpc("PEP", 33873)
            if rpc_bh > 0:
                bh = rpc_bh

        halvings = bh // 100000
        reward = 50 / (2 ** min(halvings, 10))  # Cap at 10 halvings to prevent underflow
        return {"block_height": bh, "scrypt_reward": reward, "symbol": "PEP"}

    def _fetch_cat():
        # Catcoin (CAT): 25 CAT initial reward (like Bitcoin halving schedule)
        # 10-minute block time (like Bitcoin), halving every 210,000 blocks
        # Launched December 2013
        bh = 700000  # Default fallback

        # Method 1: Pool API (if mining CAT)
        pool_bh = _get_block_height_from_pool("CAT")
        if pool_bh > 0:
            bh = pool_bh
        else:
            # Method 2: Direct RPC to Catcoin daemon (default port 9932)
            rpc_bh = _get_block_height_from_rpc("CAT", 9932)
            if rpc_bh > 0:
                bh = rpc_bh

        halvings = bh // 210000
        reward = 25 / (2 ** halvings)  # Initial 25 CAT, halved each cycle
        return {"block_height": bh, "scrypt_reward": reward, "symbol": "CAT"}

    def _fetch_fbtc():
        # Fractal Bitcoin (FBTC): 25 FBTC initial reward (Bitcoin halving schedule)
        # 30-second block time, halving every 2,100,000 blocks (~2 years)
        bh = 500000  # Default fallback

        # Method 1: Pool API (if mining FBTC)
        pool_bh = _get_block_height_from_pool("FBTC")
        if pool_bh > 0:
            bh = pool_bh
        else:
            # Method 2: Direct RPC to Fractal daemon (default port 8340)
            rpc_bh = _get_block_height_from_rpc("FBTC", 8340)
            if rpc_bh > 0:
                bh = rpc_bh

        halvings = bh // 2100000
        reward = 25 / (2 ** min(halvings, 10))
        return {"block_height": bh, "sha256_reward": reward, "symbol": "FBTC"}

    # SHA-256d coins
    if symbol == "DGB":
        return _cached_fetch("block_reward_dgb", _fetch_dgb, 43200)
    elif symbol == "BTC":
        return _cached_fetch("block_reward_btc", _fetch_btc, 43200)
    elif symbol == "BCH":
        return _cached_fetch("block_reward_bch", _fetch_bch, 43200)
    elif symbol == "BC2":
        return _cached_fetch("block_reward_bc2", _fetch_bc2, 43200)
    elif symbol == "QBX":
        return _cached_fetch("block_reward_qbx", _fetch_qbx, 43200)
    # SHA-256d merge-mineable coins
    elif symbol == "NMC":
        return _cached_fetch("block_reward_nmc", _fetch_nmc, 43200)
    elif symbol == "SYS":
        return _cached_fetch("block_reward_sys", _fetch_sys, 43200)
    elif symbol == "XMY":
        return _cached_fetch("block_reward_xmy", _fetch_xmy, 43200)
    elif symbol == "FBTC":
        return _cached_fetch("block_reward_fbtc", _fetch_fbtc, 43200)
    # Scrypt coins
    elif symbol == "LTC":
        return _cached_fetch("block_reward_ltc", _fetch_ltc, 43200)
    elif symbol == "DOGE":
        return _cached_fetch("block_reward_doge", _fetch_doge, 43200)
    elif symbol == "DGB-SCRYPT":
        return _cached_fetch("block_reward_dgb", _fetch_dgb, 43200)  # Same chain as DGB
    elif symbol == "PEP":
        return _cached_fetch("block_reward_pep", _fetch_pep, 43200)
    elif symbol == "CAT":
        return _cached_fetch("block_reward_cat", _fetch_cat, 43200)
    return None

def fetch_power_cost():
    """Fetch electricity cost data from Spiral Dash API.

    Returns dict with keys: daily_cost, monthly_cost, daily_kwh, monthly_kwh,
    currency_symbol, is_free_power, daily_profit, monthly_profit, profit_margin_percent.
    Returns None if dashboard is unreachable.
    """
    try:
        pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
        dash_url = pool_url.replace(":4000", ":1618")
        data = _http(f"{dash_url}/api/power/stats", timeout=5)
        if not data:
            return None
        cost = data.get("cost", {})
        power = data.get("power", {})
        profit = data.get("profitability", {})
        return {
            "daily_cost": cost.get("daily_cost", 0),
            "monthly_cost": cost.get("monthly_cost", 0),
            "daily_kwh": power.get("daily_kwh", 0),
            "monthly_kwh": power.get("monthly_kwh", 0),
            "currency_symbol": cost.get("currency_symbol", "$"),
            "is_free_power": cost.get("is_free_power", False),
            "daily_profit": profit.get("daily_profit", 0),
            "monthly_profit": profit.get("monthly_profit", 0),
            "profit_margin_percent": profit.get("profit_margin_percent", 0),
        }
    except Exception:
        return None


def fetch_wallet_balance(addr):
    """Fetch DGB wallet balance (legacy function for backwards compatibility)."""
    return fetch_wallet_balance_for_coin(addr, "DGB")

def fetch_wallet_balance_for_coin(addr, symbol):
    """Fetch wallet balance for a specific coin (DGB, BTC, or BCH)."""
    if not addr or "YOUR" in addr: return None
    symbol = symbol.upper()

    # SECURITY: Validate wallet address format (alphanumeric + colon for CashAddr like bitcoincash:q...)
    if not re.match(r'^[a-zA-Z0-9:]+$', addr):
        logger.warning(f"SECURITY: Rejecting wallet address with invalid characters: {addr[:20]}...")
        return None

    # SECURITY: URL-encode address to prevent injection in API URLs
    safe_addr = url_quote(addr, safe='')

    try:
        if symbol == "DGB":
            req = urllib.request.Request(f"https://chainz.cryptoid.info/dgb/api.dws?q=getbalance&a={safe_addr}", headers={"User-Agent": f"SpiralSentinel/{__version__}"})
            with urllib.request.urlopen(req, timeout=10) as resp: return float(resp.read().decode().strip())
        elif symbol == "BTC":
            # Use blockchain.info API for BTC balance
            data = _http(f"https://blockchain.info/q/addressbalance/{safe_addr}")
            if data is not None: return float(data) / 1e8  # Convert satoshis to BTC
        elif symbol == "BCH":
            # Use blockchair API for BCH balance
            data = _http(f"https://api.blockchair.com/bitcoin-cash/dashboards/address/{safe_addr}")
            if data and data.get("data", {}).get(addr, {}).get("address", {}).get("balance"):
                return float(data["data"][addr]["address"]["balance"]) / 1e8  # Convert satoshis to BCH
    except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError, ValueError, KeyError, TypeError):
        pass
    return None

# === HISTORICAL DATA MANAGER ===
# Stores 2 years of data at 15-minute intervals
# ~70,000 samples per metric = ~5MB per metric in JSON
HISTORY_FILE = DATA_DIR / "history.json"
HISTORY_SAMPLE_INTERVAL = 900  # 15 minutes
HISTORY_MAX_AGE = 63072000  # 2 years in seconds

class HistoricalDataManager:
    """Manages long-term historical data for difficulty and hashrate trends.

    Supports multi-coin tracking with per-coin difficulty and network hashrate history.
    Fleet hashrate is shared across all coins (same miners, different algorithms tracked separately).
    """

    def __init__(self):
        self.data_dir = DATA_DIR
        try:
            self.data_dir.mkdir(parents=True, exist_ok=True)
        except (PermissionError, OSError):
            pass  # Directory already handled at module level
        self.history_file = self.data_dir / "history.json"
        self.last_sample_time = {}  # Per-coin last sample time
        self.active_coin = None  # Current coin for volatility threshold
        self._load()

    def _load(self):
        """Load historical data from disk.

        Supports both legacy format (single lists) and new multi-coin format (dicts of lists).
        Legacy data is migrated to the primary coin on first load.
        """
        # Multi-coin structure: {"DGB": [{t, diff}], "BC2": [{t, diff}], ...}
        self.difficulty_history = {}  # {coin: [{t, diff}]}
        self.network_phs_history = {}  # {coin: [{t, phs}]}
        self.fleet_ths_history = []  # [{t, ths}] - shared across coins
        self.ema_cache = {}  # Cached EMA values

        if self.history_file.exists():
            try:
                with open(self.history_file) as f:
                    d = json.load(f)

                # Check if this is legacy format (lists) or new format (dicts)
                diff_data = d.get("difficulty", {})
                net_data = d.get("network_phs", {})

                if isinstance(diff_data, list):
                    # Legacy format - migrate to primary coin
                    primary = get_primary_coin() or "UNKNOWN"
                    logger.info(f"Migrating legacy history data to coin: {primary}")
                    self.difficulty_history = {primary: diff_data} if diff_data else {}
                    self.network_phs_history = {primary: net_data} if net_data else {}
                else:
                    # New multi-coin format
                    self.difficulty_history = diff_data
                    self.network_phs_history = net_data

                self.fleet_ths_history = d.get("fleet_ths", [])

                # Handle per-coin last_sample_time
                last_sample = d.get("last_sample", 0)
                if isinstance(last_sample, dict):
                    self.last_sample_time = last_sample
                else:
                    # Legacy single timestamp - apply to all known coins
                    for coin in self.difficulty_history.keys():
                        self.last_sample_time[coin] = last_sample

                self._prune_old_data()
            except (json.JSONDecodeError, IOError, OSError, KeyError) as e:
                logger.warning(f"Could not load history file: {e}")

    def save(self):
        """Save historical data to disk in multi-coin format (atomic write)"""
        _atomic_json_save(self.history_file, {
            "difficulty": self.difficulty_history,
            "network_phs": self.network_phs_history,
            "fleet_ths": self.fleet_ths_history,
            "last_sample": self.last_sample_time,
            "updated": time.time(),
            "version": 2  # Multi-coin format version
        })

    def _prune_old_data(self):
        """Remove data older than 2 years for all coins"""
        cutoff = time.time() - HISTORY_MAX_AGE

        # Prune per-coin difficulty history
        for coin in list(self.difficulty_history.keys()):
            self.difficulty_history[coin] = [x for x in self.difficulty_history[coin] if x.get("t", 0) > cutoff]
            if not self.difficulty_history[coin]:
                del self.difficulty_history[coin]

        # Prune per-coin network hashrate history
        for coin in list(self.network_phs_history.keys()):
            self.network_phs_history[coin] = [x for x in self.network_phs_history[coin] if x.get("t", 0) > cutoff]
            if not self.network_phs_history[coin]:
                del self.network_phs_history[coin]

        # Prune fleet hashrate (shared)
        self.fleet_ths_history = [x for x in self.fleet_ths_history if x.get("t", 0) > cutoff]

    def should_sample(self, coin=None):
        """Check if enough time has passed for a new sample for the given coin"""
        coin = (coin or self.active_coin or get_primary_coin() or "UNKNOWN").upper()
        last = self.last_sample_time.get(coin, 0)
        return (time.time() - last) >= HISTORY_SAMPLE_INTERVAL

    def record_sample(self, difficulty, network_phs, fleet_ths, coin=None):
        """Record a new 15-minute sample for the specified coin.

        Args:
            difficulty: Network difficulty for the coin
            network_phs: Network hashrate in PH/s for the coin
            fleet_ths: Fleet hashrate in TH/s (shared across coins)
            coin: Coin symbol (e.g., "DGB", "BC2", "BTC"). If None, uses primary coin.

        Returns:
            True if a sample was recorded, False if skipped (too soon).
        """
        coin = (coin or self.active_coin or get_primary_coin() or "UNKNOWN").upper()

        if not self.should_sample(coin):
            return False

        t = time.time()
        self.last_sample_time[coin] = t

        # Record per-coin difficulty
        if difficulty and difficulty > 0:
            if coin not in self.difficulty_history:
                self.difficulty_history[coin] = []
            self.difficulty_history[coin].append({"t": t, "diff": difficulty})
            logger.debug(f"Recorded difficulty sample for {coin}: {format_difficulty(difficulty)}")

        # Record per-coin network hashrate
        if network_phs and network_phs > 0:
            if coin not in self.network_phs_history:
                self.network_phs_history[coin] = []
            self.network_phs_history[coin].append({"t": t, "phs": network_phs})

        # Record fleet hashrate (shared across all coins)
        if fleet_ths and fleet_ths > 0:
            self.fleet_ths_history.append({"t": t, "ths": fleet_ths})

        # Prune old data periodically (check total samples across all coins)
        total_samples = sum(len(v) for v in self.difficulty_history.values())
        if total_samples % 100 == 0:
            self._prune_old_data()

        # Clear EMA cache when new data arrives
        self.ema_cache = {}
        return True

    def _calc_ema(self, data, periods):
        """Calculate Exponential Moving Average
        periods = number of samples to use for EMA
        """
        if not data or len(data) < 2:
            return None

        # Get the value key
        value_key = "diff" if "diff" in data[0] else ("phs" if "phs" in data[0] else "ths")
        values = [x[value_key] for x in data[-periods:] if value_key in x]

        if not values:
            return None

        # EMA formula: EMA = (Value * k) + (Previous EMA * (1 - k))
        # where k = 2 / (periods + 1)
        k = 2 / (len(values) + 1)
        ema = values[0]
        for v in values[1:]:
            ema = (v * k) + (ema * (1 - k))

        return ema

    def get_trend_data(self, metric, hours, coin=None):
        """Get trend data for a specific metric, time period, and coin.

        Args:
            metric: "difficulty", "network_phs", or "fleet_ths"
            hours: Number of hours to look back
            coin: Coin symbol (required for difficulty/network_phs, ignored for fleet_ths)

        Returns: {current, avg, ema, trend, pct_change, samples, min, max}
        trend: 'rising', 'falling', or 'flat'
        """
        coin = (coin or self.active_coin or get_primary_coin() or "UNKNOWN").upper()

        if metric == "difficulty":
            data = self.difficulty_history.get(coin, [])
            value_key = "diff"
        elif metric == "network_phs":
            data = self.network_phs_history.get(coin, [])
            value_key = "phs"
        elif metric == "fleet_ths":
            data = self.fleet_ths_history
            value_key = "ths"
        else:
            return None

        now = time.time()
        cutoff = now - (hours * 3600)
        samples = [x for x in data if x.get("t", 0) > cutoff]

        if not samples:
            return None

        # Require minimum data coverage: oldest sample must span at least 25%
        # of the requested period.  Without this, a "30d" trend based on only
        # 3 hours of data would show a misleading near-zero change instead of
        # N/A.  The 6h window is exempt because it's the shortest useful period
        # and we want to show *something* once we have any data at all.
        if hours > 6 and len(samples) >= 2:
            oldest_age = now - samples[0].get("t", now)
            required_age = hours * 3600 * 0.25  # 25% of the requested window
            if oldest_age < required_age:
                return None

        values = [x.get(value_key, 0) for x in samples if value_key in x]
        current = values[-1] if values else 0
        avg = sum(values) / len(values) if values else 0

        # Calculate EMA (use ~1/4 of samples as period for smoothing)
        ema_periods = max(4, len(values) // 4)
        ema = self._calc_ema(samples, ema_periods)

        # Determine trend by comparing current to EMA
        # Use coin-specific volatility threshold (DGB=5%, BTC=2%, BCH=3%)
        # SHA-256 networks have different volatility due to profit-switching miners
        volatility_threshold = get_coin_volatility_threshold(coin)
        if ema:
            pct_diff = ((current - ema) / ema) * 100 if ema > 0 else 0
            if pct_diff > volatility_threshold:
                trend = "rising"
            elif pct_diff < -volatility_threshold:
                trend = "falling"
            else:
                trend = "flat"
        else:
            trend = "flat"

        # pct_change: compare first sample to last sample in the window
        # This shows the actual change over the period (e.g., "24h: +2.3%" means
        # difficulty rose 2.3% from 24 hours ago to now), not current-vs-average
        # which washes out gradual trends (especially for DGB's per-block retarget)
        if len(values) >= 2 and values[0] > 0:
            pct_change = ((values[-1] - values[0]) / values[0]) * 100
        else:
            pct_change = 0

        return {
            "current": current,
            "avg": avg,
            "ema": ema,
            "trend": trend,
            "pct_change": pct_change,
            "samples": len(samples),
            "min": min(values) if values else 0,
            "max": max(values) if values else 0
        }

    def get_multi_period_trends(self, metric, coin=None):
        """Get trends for 6h, 1d, 2d, 3d, 5d, 7d, 30d periods for the specified coin.

        Args:
            metric: "difficulty", "network_phs", or "fleet_ths"
            coin: Coin symbol (required for difficulty/network_phs, ignored for fleet_ths)
        """
        periods = {
            "6h": 6,
            "12h": 12,
            "1d": 24,
            "2d": 48,
            "3d": 72,
            "5d": 120,
            "7d": 168,
            "30d": 720
        }

        trends = {}
        for name, hours in periods.items():
            trends[name] = self.get_trend_data(metric, hours, coin)

        return trends

    def get_summary_stats(self, coin=None):
        """Get summary statistics for all metrics for the specified coin"""
        coin = (coin or self.active_coin or get_primary_coin() or "UNKNOWN").upper()
        diff_hist = self.difficulty_history.get(coin, [])
        net_hist = self.network_phs_history.get(coin, [])

        return {
            "coin": coin,
            "difficulty": {
                "samples": len(diff_hist),
                "oldest": datetime.fromtimestamp(diff_hist[0]["t"], tz=timezone.utc).isoformat() if diff_hist else None,
                "newest": datetime.fromtimestamp(diff_hist[-1]["t"], tz=timezone.utc).isoformat() if diff_hist else None
            },
            "network_phs": {
                "samples": len(net_hist),
                "oldest": datetime.fromtimestamp(net_hist[0]["t"], tz=timezone.utc).isoformat() if net_hist else None,
                "newest": datetime.fromtimestamp(net_hist[-1]["t"], tz=timezone.utc).isoformat() if net_hist else None
            },
            "fleet_ths": {
                "samples": len(self.fleet_ths_history),
                "oldest": datetime.fromtimestamp(self.fleet_ths_history[0]["t"], tz=timezone.utc).isoformat() if self.fleet_ths_history else None,
                "newest": datetime.fromtimestamp(self.fleet_ths_history[-1]["t"], tz=timezone.utc).isoformat() if self.fleet_ths_history else None
            },
            "all_coins": list(self.difficulty_history.keys())
        }

# === SPECIAL DATE DETECTION ===
def _get_nth_weekday_of_month(year, month, weekday, n):
    """Get the nth occurrence of a weekday (0=Monday) in a month.
    n=1 is first, n=-1 is last."""
    from calendar import monthrange
    if n > 0:
        # Find first occurrence of weekday in month
        first_day = datetime(year, month, 1)
        first_weekday = first_day.weekday()
        # Days until first occurrence of target weekday
        days_until = (weekday - first_weekday) % 7
        first_occurrence = 1 + days_until
        day = first_occurrence + (n - 1) * 7
    else:  # n == -1 (last occurrence)
        _, last_day = monthrange(year, month)
        last = datetime(year, month, last_day)
        last_weekday = last.weekday()
        days_back = (last_weekday - weekday) % 7
        day = last_day - days_back
    return day


def _compute_equinoxes_solstices(year):
    """Compute equinox/solstice dates for a given year using the Meeus algorithm.
    Returns dict of (month, day) -> event info. Accurate to within ~1 day for 2000-2100."""
    import math
    # Julian Ephemeris Day approximations (Meeus, Astronomical Algorithms ch. 27)
    # Y = (year - 2000) / 1000
    y = (year - 2000) / 1000.0
    # JDE0 for March equinox, June solstice, September equinox, December solstice
    jde_march    = 2451623.80984 + 365242.37404 * y + 0.05169 * y**2 - 0.00411 * y**3 - 0.00057 * y**4
    jde_june     = 2451716.56767 + 365241.62603 * y + 0.00325 * y**2 + 0.00888 * y**3 - 0.00030 * y**4
    jde_sept     = 2451810.21715 + 365242.01767 * y - 0.11575 * y**2 + 0.00337 * y**3 + 0.00078 * y**4
    jde_dec      = 2451900.05952 + 365242.74049 * y - 0.06223 * y**2 - 0.00823 * y**3 + 0.00032 * y**4

    def _jde_to_date(jde):
        """Convert Julian Ephemeris Day to (month, day) in UTC."""
        # Standard Julian Day to calendar date conversion
        jd = jde + 0.5
        z = int(jd)
        f = jd - z
        if z < 2299161:
            a = z
        else:
            alpha = int((z - 1867216.25) / 36524.25)
            a = z + 1 + alpha - int(alpha / 4)
        b = a + 1524
        c = int((b - 122.1) / 365.25)
        d = int(365.25 * c)
        e = int((b - d) / 30.6001)
        day = b - d - int(30.6001 * e)
        month = e - 1 if e < 14 else e - 13
        return (month, day)

    events = [
        (jde_march, {"name": "Spring Equinox", "emoji": "\U0001f338", "type": "equinox"}),
        (jde_june,  {"name": "Summer Solstice", "emoji": "\u2600\ufe0f", "type": "solstice"}),
        (jde_sept,  {"name": "Autumn Equinox", "emoji": "\U0001f342", "type": "equinox"}),
        (jde_dec,   {"name": "Winter Solstice", "emoji": "\u2744\ufe0f", "type": "solstice"}),
    ]
    result = {}
    for jde, info in events:
        md = _jde_to_date(jde)
        result[md] = info
    return result


def _compute_easter(year):
    """Compute Easter Sunday date using the Anonymous Gregorian algorithm (Meeus).
    Returns (month, day)."""
    a = year % 19
    b = year // 100
    c = year % 100
    d = b // 4
    e = b % 4
    f = (b + 8) // 25
    g = (b - f + 1) // 3
    h = (19 * a + b - d - g + 15) % 30
    i = c // 4
    k = c % 4
    l = (32 + 2 * e + 2 * i - h - k) % 7
    m = (a + 11 * h + 22 * l) // 451
    month = (h + l - 7 * m + 114) // 31
    day = ((h + l - 7 * m + 114) % 31) + 1
    return (month, day)


def get_special_date_info():
    """Check if today is a special date for reports.
    Holidays are based on POWER_CURRENCY: CAD=Canadian, USD=American.
    Equinoxes/solstices and Easter are computed astronomically — not hardcoded.
    """
    now = local_now()
    year, month, day = now.year, now.month, now.day

    # === UNIVERSAL DATES ===
    special_dates = {
        (1, 1): {"name": "New Year's Day", "emoji": "🎆", "type": "holiday", "theme_key": "new_years"},
        (12, 25): {"name": "Christmas", "emoji": "🎄", "type": "holiday", "theme_key": "christmas"},
    }

    # Equinoxes and solstices — astronomically computed per year
    for md, info in _compute_equinoxes_solstices(year).items():
        # Derive theme_key from name: "Spring Equinox" -> "spring_equinox"
        info["theme_key"] = info["name"].lower().replace(" ", "_")
        special_dates[md] = info

    # Easter and Good Friday — computed per year
    easter_month, easter_day = _compute_easter(year)
    easter_date = datetime(year, easter_month, easter_day)
    good_friday = easter_date - timedelta(days=2)
    special_dates[(easter_month, easter_day)] = {"name": "Easter", "emoji": "🐣", "type": "holiday", "theme_key": "easter"}
    special_dates[(good_friday.month, good_friday.day)] = {"name": "Good Friday", "emoji": "✝️", "type": "holiday", "theme_key": "good_friday"}

    # === CANADIAN HOLIDAYS (power_currency = CAD) ===
    if POWER_CURRENCY == "CAD":
        special_dates[(7, 1)] = {"name": "Canada Day", "emoji": "🍁", "type": "holiday", "theme_key": "canada_day"}
        # Victoria Day - last Monday on or before May 24
        if month == 5:
            victoria_day = 24 - datetime(year, 5, 24).weekday()
            if day == victoria_day:
                return {"name": "Victoria Day", "emoji": "👑", "type": "holiday", "theme_key": "victoria_day"}
        if month == 8 and day == _get_nth_weekday_of_month(year, 8, 0, 1):
            return {"name": "Civic Holiday", "emoji": "🏖️", "type": "holiday", "theme_key": "civic_holiday"}
        if month == 9 and day == _get_nth_weekday_of_month(year, 9, 0, 1):
            return {"name": "Labour Day", "emoji": "⚒️", "type": "holiday", "theme_key": "labour_day"}
        if month == 10 and day == _get_nth_weekday_of_month(year, 10, 0, 2):
            return {"name": "Thanksgiving", "emoji": "🦃", "type": "holiday", "theme_key": "thanksgiving_ca"}
        # National Indigenous Peoples Day - June 21 (may overlap Summer Solstice — holiday takes priority)
        special_dates[(6, 21)] = {"name": "National Indigenous Peoples Day", "emoji": "🪶", "type": "holiday", "theme_key": "indigenous_day"}
        # Truth and Reconciliation Day - September 30 (federal stat since 2021)
        special_dates[(9, 30)] = {"name": "Truth & Reconciliation Day", "emoji": "🧡", "type": "holiday", "theme_key": "truth_reconciliation"}
        special_dates[(11, 11)] = {"name": "Remembrance Day", "emoji": "🌺", "type": "holiday", "theme_key": "remembrance_day"}
        special_dates[(12, 26)] = {"name": "Boxing Day", "emoji": "🎁", "type": "holiday", "theme_key": "boxing_day"}
        if month == 2 and day == _get_nth_weekday_of_month(year, 2, 0, 3):
            return {"name": "Family Day", "emoji": "👨‍👩‍👧‍👦", "type": "holiday", "theme_key": "family_day"}

    # === AMERICAN HOLIDAYS (power_currency = USD) ===
    elif POWER_CURRENCY == "USD":
        special_dates[(7, 4)] = {"name": "Independence Day", "emoji": "🦅", "type": "holiday", "theme_key": "independence_day"}
        # Juneteenth - June 19 (federal holiday since 2021)
        special_dates[(6, 19)] = {"name": "Juneteenth", "emoji": "✊🏿", "type": "holiday", "theme_key": "juneteenth"}
        if month == 1 and day == _get_nth_weekday_of_month(year, 1, 0, 3):
            return {"name": "MLK Day", "emoji": "✊", "type": "holiday", "theme_key": "mlk_day"}
        if month == 2 and day == _get_nth_weekday_of_month(year, 2, 0, 3):
            return {"name": "Presidents' Day", "emoji": "🏛️", "type": "holiday", "theme_key": "presidents_day"}
        if month == 5 and day == _get_nth_weekday_of_month(year, 5, 0, -1):
            return {"name": "Memorial Day", "emoji": "🎖️", "type": "holiday", "theme_key": "memorial_day"}
        if month == 9 and day == _get_nth_weekday_of_month(year, 9, 0, 1):
            return {"name": "Labor Day", "emoji": "⚒️", "type": "holiday", "theme_key": "labor_day"}
        if month == 10 and day == _get_nth_weekday_of_month(year, 10, 0, 2):
            return {"name": "Columbus Day", "emoji": "🚢", "type": "holiday", "theme_key": "columbus_day"}
        if month == 11 and day == _get_nth_weekday_of_month(year, 11, 3, 4):
            return {"name": "Thanksgiving", "emoji": "🦃", "type": "holiday", "theme_key": "thanksgiving_us"}
        special_dates[(11, 11)] = {"name": "Veterans Day", "emoji": "🎖️", "type": "holiday", "theme_key": "veterans_day"}

    return special_dates.get((month, day))

def is_quarter_end():
    """Check if today is the end of a quarter"""
    now = local_now()
    quarter_ends = [(3, 31), (6, 30), (9, 30), (12, 31)]
    return (now.month, now.day) in quarter_ends

def get_quarter_name():
    """Get the current quarter name"""
    now = local_now()
    quarter = (now.month - 1) // 3 + 1
    return f"Q{quarter} {now.year}"

# === CALCULATIONS ===
def calc_odds(net_phs, fleet_ths, coin=None):
    """Calculate block finding odds for the specified coin.

    Args:
        net_phs: Network hashrate in PH/s
        fleet_ths: Fleet hashrate in TH/s
        coin: Coin symbol for block time (DGB=75s, BTC/BCH=600s). If None, uses primary coin.

    Returns:
        dict with share_pct, daily_odds_pct, weekly_odds_pct, days_per_block
    """
    if coin is None:
        coin = get_primary_coin()
    if coin is None:
        # Cannot calculate without knowing the coin
        return {"share_pct": 0, "daily_odds_pct": 0, "weekly_odds_pct": 0, "days_per_block": float('inf')}

    # Blocks per day depends on coin's target block time
    # 86400 seconds/day divided by block time
    coin_blocks_per_day = {
        # SHA-256d coins
        "DGB": 1152,       # 86400 / 75 = 1152 (75s target, but 15s actual for SHA256d algo)
        "BTC": 144,        # 86400 / 600 = 144
        "BCH": 144,        # 86400 / 600 = 144
        "BC2": 144,        # 86400 / 600 = 144
        # SHA-256d merge-mineable aux chains
        "NMC": 144,        # 86400 / 600 = 144 (same as Bitcoin)
        "SYS": 1440,       # 86400 / 60 = 1440 (60s block time)
        "XMY": 1440,       # 86400 / 60 = 1440 (60s per algo, 5 algos)
        "FBTC": 2880,      # 86400 / 30 = 2880 (30s block time)
        "QBX": 576,        # 86400 / 150 = 576 (150s block time)
        # Scrypt coins
        "LTC": 576,        # 86400 / 150 = 576
        "DOGE": 1440,      # 86400 / 60 = 1440
        "DGB-SCRYPT": 1152, # Same as DGB SHA256d
        "PEP": 1440,       # 86400 / 60 = 1440 (60s block time)
        "CAT": 144,        # 86400 / 600 = 144 (10min block time)
    }
    blocks_per_day = coin_blocks_per_day.get(coin.upper(), 144)

    share = (fleet_ths / 1000 / net_phs * 100) if net_phs > 0 else 0
    # Clamp share to prevent NaN from probability formula when share >= 100% (e.g., regtest)
    clamped = min(share, 99.99)
    daily = 1 - ((1 - clamped/100) ** blocks_per_day)
    weekly = 1 - ((1 - clamped/100) ** (blocks_per_day * 7))
    dpb = 1 / (blocks_per_day * clamped / 100) if clamped > 0 else float('inf')
    return {"share_pct": share, "daily_odds_pct": daily * 100, "weekly_odds_pct": weekly * 100, "days_per_block": dpb}

# === PROFITABILITY TRACKER (v1.2.1 — NOT ACTIVE) ===
# This module computes per-coin profitability rankings within each algorithm family.
# It is NOT wired to any API endpoint or report loop yet. To activate in a future
# release, expose compute_profitability_rankings() via a /api/profitability route.
#
# Data sources (all existing):
#   - Prices:           fetch_all_prices() (CoinGecko, 2min cache)
#   - Network stats:    fetch_network_stats(coin) (difficulty, hashrate)
#   - Block rewards:    DEFAULT_BLOCK_REWARDS (fallback) + live from pool API
#   - Blocks per day:   COIN_BLOCKS_PER_DAY (derived from block time)
#   - Algorithm family: COIN_ALGORITHM_FAMILY

COIN_BLOCKS_PER_DAY = {
    # SHA-256d
    "DGB": 1152, "BTC": 144, "BCH": 144, "BC2": 144,
    "NMC": 144, "SYS": 1440, "XMY": 1440, "FBTC": 2880, "QBX": 576,
    # Scrypt
    "LTC": 576, "DOGE": 1440, "DGB-SCRYPT": 1152, "PEP": 1440, "CAT": 144,
}

COIN_ALGORITHM_FAMILY = {
    "DGB": "sha256d", "BTC": "sha256d", "BCH": "sha256d", "BC2": "sha256d",
    "NMC": "sha256d", "SYS": "sha256d", "XMY": "sha256d", "FBTC": "sha256d", "QBX": "sha256d",
    "LTC": "scrypt", "DOGE": "scrypt", "DGB-SCRYPT": "scrypt", "PEP": "scrypt", "CAT": "scrypt",
}

# Price key mapping: coin symbol -> key prefix in fetch_all_prices() result
_PRICE_KEY_MAP = {
    "DGB": "dgb", "BTC": "btc", "BCH": "bch", "BC2": "bc2", "QBX": "qbx",
    "NMC": "nmc", "SYS": "sys", "XMY": "xmy", "FBTC": "fbtc",
    "LTC": "ltc", "DOGE": "doge", "DGB-SCRYPT": "dgb-scrypt", "PEP": "pep", "CAT": "cat",
}


def compute_coin_profitability(coin, prices, currency_code, hashrate_ths=1.0):
    """Compute estimated daily revenue for a single coin at a given hashrate.

    Args:
        coin:           Coin symbol (e.g. "BTC", "DGB", "LTC")
        prices:         Dict from fetch_all_prices()
        currency_code:  Lowercase currency code (e.g. "cad", "usd")
        hashrate_ths:   Hashrate to calculate for, in TH/s (default 1.0)

    Returns:
        dict with profitability data, or None if data unavailable
    """
    coin = coin.upper()
    if coin not in COIN_BLOCKS_PER_DAY:
        return None

    # Get network stats (difficulty + hashrate)
    net_stats = fetch_network_stats(coin)
    if not net_stats or net_stats.get("network_phs", 0) <= 0:
        return None

    net_phs = net_stats["network_phs"]
    difficulty = net_stats.get("difficulty", 0)

    # Block reward — try live from pool API, fall back to default
    block_reward = DEFAULT_BLOCK_REWARDS.get(coin, 0)
    pool_stats = fetch_pool_stats_by_symbol(coin)
    if pool_stats:
        live_reward = pool_stats.get("poolStats", {}).get("blockReward", 0)
        if live_reward > 0:
            block_reward = live_reward

    if block_reward <= 0:
        return None

    # Coin price in user's currency
    price_key = f"{_PRICE_KEY_MAP.get(coin, coin.lower())}_{currency_code}"
    coin_price = prices.get(price_key, 0) if prices else 0

    # Core calculation: expected coins per day at given hashrate
    # share_of_network = hashrate_ths / (net_phs * 1000)  [convert PH/s to TH/s]
    # daily_coins = block_reward * blocks_per_day * share_of_network
    blocks_per_day = COIN_BLOCKS_PER_DAY[coin]
    net_ths = net_phs * 1000.0
    share_pct = (hashrate_ths / net_ths * 100) if net_ths > 0 else 0
    daily_coins = block_reward * blocks_per_day * (hashrate_ths / net_ths) if net_ths > 0 else 0
    daily_fiat = daily_coins * coin_price

    # Sats value (how many sats per day — useful for cross-coin comparison)
    sats_key = f"{_PRICE_KEY_MAP.get(coin, coin.lower())}_sats"
    coin_sats = prices.get(sats_key, 0) if prices else 0

    return {
        "coin": coin,
        "algorithm": COIN_ALGORITHM_FAMILY.get(coin, "unknown"),
        "block_reward": block_reward,
        "blocks_per_day": blocks_per_day,
        "network_phs": net_phs,
        "difficulty": difficulty,
        "hashrate_ths": hashrate_ths,
        "share_pct": share_pct,
        "daily_coins": daily_coins,
        "coin_price_fiat": coin_price,
        "daily_fiat": daily_fiat,
        "daily_sats": daily_coins * coin_sats if coin != "BTC" else int(daily_coins * 1e8),
        "currency": currency_code.upper(),
    }


def compute_profitability_rankings(hashrate_ths=1.0):
    """Compute profitability rankings for all coins, grouped by algorithm family.

    Ranks coins within each algorithm family (sha256d, scrypt) by estimated daily
    fiat revenue at the given hashrate. Coins with no price or network data are
    excluded rather than ranked at zero.

    Args:
        hashrate_ths: Hashrate to calculate for, in TH/s (default 1.0)

    Returns:
        dict with structure:
        {
            "hashrate_ths": 1.0,
            "currency": "CAD",
            "timestamp": "2026-03-21T06:00:00",
            "sha256d": [
                {"rank": 1, "coin": "FBTC", "daily_fiat": 12.34, ...},
                {"rank": 2, "coin": "DGB", "daily_fiat": 8.56, ...},
                ...
            ],
            "scrypt": [
                {"rank": 1, "coin": "DOGE", "daily_fiat": 5.67, ...},
                ...
            ]
        }
    """
    prices = fetch_all_prices()
    cur = get_currency_meta()
    currency_code = cur.get("code", "usd")

    families = {"sha256d": [], "scrypt": []}

    for coin in COIN_BLOCKS_PER_DAY:
        result = compute_coin_profitability(coin, prices, currency_code, hashrate_ths)
        if result is None:
            continue
        family = result["algorithm"]
        if family in families:
            families[family].append(result)

    # Sort each family by daily_fiat descending (most profitable first)
    for family in families:
        families[family].sort(key=lambda x: x["daily_fiat"], reverse=True)
        for i, entry in enumerate(families[family]):
            entry["rank"] = i + 1

    return {
        "hashrate_ths": hashrate_ths,
        "currency": currency_code.upper(),
        "currency_symbol": cur.get("symbol", "$"),
        "timestamp": local_now().isoformat(),
        "sha256d": families["sha256d"],
        "scrypt": families["scrypt"],
    }
# === END PROFITABILITY TRACKER ===


def get_status_level(net_phs, coin=None):
    """Get status level based on network hashrate for a specific coin."""
    if coin is None:
        coin = get_primary_coin()
    thresholds = get_network_thresholds(coin)
    for lv, cfg in thresholds.items():
        if net_phs < cfg["max"]: return lv, cfg["emoji"]
    return "HIGH", "🔴"

# === NOTIFICATIONS ===
def is_quiet_hours():
    if not QUIET_HOURS_ENABLED: return False
    h = local_now().hour
    if QUIET_START < QUIET_END: return QUIET_START <= h < QUIET_END
    return h >= QUIET_START or h < QUIET_END


def is_in_maintenance_window():
    """Check if the current time falls within a scheduled maintenance window.

    Returns (True, reason) if in a window, (False, None) otherwise.
    Each window in config: {"start": "HH:MM", "end": "HH:MM", "days": [0-6], "reason": "..."}
    days is optional; 0=Monday, 6=Sunday. Omit to apply every day.
    Supports overnight windows (e.g., 23:00–01:00).
    """
    windows = CONFIG.get("scheduled_maintenance_windows", [])
    if not windows:
        return False, None
    now = local_now()
    current_dow = now.weekday()  # 0=Monday, 6=Sunday
    current_hm = now.hour * 60 + now.minute
    for window in windows:
        try:
            days = window.get("days")
            if days is not None and current_dow not in days:
                continue
            start_h, start_m = map(int, window["start"].split(":"))
            end_h, end_m = map(int, window["end"].split(":"))
            start_min = start_h * 60 + start_m
            end_min = end_h * 60 + end_m
            reason = window.get("reason", "Scheduled maintenance")
            if start_min <= end_min:
                if start_min <= current_hm < end_min:
                    return True, reason
            else:
                # Overnight window
                if current_hm >= start_min or current_hm < end_min:
                    return True, reason
        except (KeyError, ValueError, AttributeError):
            continue
    return False, None


def check_for_updates():
    """Check for Spiral Pool updates by calling upgrade.sh --check"""
    if not UPDATE_CHECK_ENABLED:
        return None

    try:
        import subprocess
        result = subprocess.run(
            [str(INSTALL_DIR / "upgrade.sh"), "--check"],
            capture_output=True,
            text=True,
            timeout=30
        )
        if result.returncode == 0 and result.stdout:
            return json.loads(result.stdout)
    except Exception as e:
        logger.debug(f"Update check error: {e}")
    return None

def perform_auto_update(update_info):
    """Run upgrade.sh --auto when auto_update_mode is 'auto'. Returns True if launched."""
    latest_ver = update_info.get("latest_version", "?")
    current_ver = update_info.get("current_version", "?")
    upgrade_script = str(INSTALL_DIR / "upgrade.sh")

    logger.info(f"AUTO-UPDATE: Launching upgrade from v{current_ver} to v{latest_ver}")

    # Notify before starting so the operator knows what's happening
    pre_fields = [
        {"name": "📦 Current Version", "value": f"`{current_ver}`", "inline": True},
        {"name": "🆕 New Version",     "value": f"`{latest_ver}`", "inline": True},
        {"name": "⚙️ Mode",            "value": "Automatic — services will restart", "inline": False},
    ]
    pre_embed = _embed(
        "⬆️ Auto-Update Starting",
        "Spiral Pool is applying an update automatically. Services will restart briefly.",
        COLORS["cyan"],
        pre_fields,
    )
    send_discord(pre_embed)
    send_telegram(
        f"⬆️ *Auto-Update Starting*\n"
        f"Upgrading from `v{current_ver}` → `v{latest_ver}`\n"
        f"Services will restart briefly."
    )

    try:
        import subprocess
        # upgrade.sh is installed at INSTALL_DIR/upgrade.sh and sudoers grants
        # POOL_USER NOPASSWD for this exact path — no password prompt needed.
        result = subprocess.run(
            ["sudo", upgrade_script, "--auto"],
            timeout=600,  # 10 min ceiling — should be well within any reasonable upgrade
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            logger.info(f"AUTO-UPDATE: Upgrade to v{latest_ver} completed successfully")
            return True
        else:
            logger.error(f"AUTO-UPDATE: upgrade.sh exited {result.returncode}: {result.stderr[:500]}")
            # Send failure alert
            fail_embed = _embed(
                "❌ Auto-Update Failed",
                f"Upgrade to `v{latest_ver}` failed (exit {result.returncode}). "
                f"Run manually: `sudo {upgrade_script}`",
                COLORS["red"],
                [{"name": "Error", "value": f"```{result.stderr[:800]}```", "inline": False}],
            )
            send_discord(fail_embed)
            send_telegram(
                f"❌ *Auto-Update Failed*\n"
                f"Upgrade to `v{latest_ver}` exited with code {result.returncode}.\n"
                f"Run manually: `sudo {upgrade_script}`"
            )
            return False
    except Exception as e:
        logger.error(f"AUTO-UPDATE: Exception running upgrade.sh: {e}")
        return False


def create_update_embed(update_info):
    """Create Discord embed for update notification"""
    current = update_info.get("current_version", "?")
    latest = update_info.get("latest_version", "?")
    upgrade_cmd = update_info.get("upgrade_command", "cd /spiralpool && sudo ./upgrade.sh")
    release_url = update_info.get("release_url", "https://github.com/SpiralPool/Spiral-Pool/releases")

    fields = [
        {"name": "📦 Current Version", "value": f"`{current}`", "inline": True},
        {"name": "🆕 Latest Version", "value": f"`{latest}`", "inline": True},
        {"name": "🔗 Release Notes", "value": f"[View on GitHub]({release_url})", "inline": False},
        {"name": "⬆️ Upgrade Command", "value": f"```bash\n{upgrade_cmd}\n```", "inline": False},
    ]

    return _embed(
        theme("update.title"),
        f"{theme('update.body')}\n\n**Run the upgrade when you're ready** - this is a notification only, no automatic updates will be performed.",
        COLORS["cyan"],
        fields,
        footer=theme("update.footer")
    )

def send_discord(embed):
    """Send Discord webhook with automatic retry on network failures."""
    # Read webhook URL fresh from config to pick up runtime changes
    # This allows users to add/change webhook URL without restarting the service
    current_config = load_config()
    webhook_url = current_config.get("discord_webhook_url", "")
    logger.debug(f"send_discord called, webhook configured: {bool(webhook_url)}")

    if not webhook_url:
        logger.warning("Discord webhook not configured (discord_webhook_url is empty in config)")
        return False
    if "YOUR" in webhook_url:
        logger.warning("Discord webhook contains placeholder 'YOUR' - please set your actual webhook URL")
        return False

    # SECURITY: Validate webhook URL to prevent URL injection attacks
    # Only allow Discord webhook URLs
    if not webhook_url.startswith("https://discord.com/api/webhooks/") and \
       not webhook_url.startswith("https://discordapp.com/api/webhooks/"):
        logger.error("SECURITY: Invalid Discord webhook URL - must start with https://discord.com/api/webhooks/")
        return False

    # SECURITY (Audit #13): Validate webhook URL path structure
    # Expected format: /api/webhooks/{numeric_id}/{alphanumeric_token}
    try:
        parsed = urlparse(webhook_url)
        path_parts = [p for p in parsed.path.split("/") if p]
        # Expected: ["api", "webhooks", "<id>", "<token>"]
        if len(path_parts) != 4 or path_parts[0] != "api" or path_parts[1] != "webhooks":
            logger.error("SECURITY: Discord webhook URL has invalid path structure")
            return False
        webhook_id, webhook_token = path_parts[2], path_parts[3]
        if not webhook_id.isdigit():
            logger.error("SECURITY: Discord webhook ID must be numeric")
            return False
        if not all(c.isalnum() or c in "-_" for c in webhook_token):
            logger.error("SECURITY: Discord webhook token contains invalid characters")
            return False
    except Exception:
        logger.error("SECURITY: Failed to parse Discord webhook URL")
        return False

    max_retries = 3
    backoff = 2  # seconds

    for attempt in range(max_retries):
        try:
            req = urllib.request.Request(webhook_url, data=json.dumps({"embeds": [embed]}).encode(), headers={
                "Content-Type": "application/json",
                "User-Agent": "SpiralSentinel/1.0"  # Discord blocks Python's default User-Agent
            })
            with urllib.request.urlopen(req, timeout=15) as resp:
                if resp.status in [200, 204]:
                    logger.debug(f"Discord webhook sent successfully (status {resp.status})")
                    return True
                else:
                    logger.warning(f"Discord webhook unexpected status: {resp.status}")
        except urllib.error.HTTPError as e:
            # Handle Discord rate limiting (429) with Retry-After header
            if e.code == 429:
                retry_after = backoff * (attempt + 1)  # Default backoff
                try:
                    # Discord returns Retry-After in seconds (can be float)
                    retry_after_header = e.headers.get("Retry-After", "")
                    if retry_after_header:
                        retry_after = float(retry_after_header) + 0.5  # Add small buffer
                except (ValueError, TypeError):
                    pass
                logger.warning(f"Discord rate limited (429), waiting {retry_after:.1f}s (attempt {attempt + 1}/{max_retries})")
                if attempt < max_retries - 1:
                    time.sleep(retry_after)
                    continue
                logger.error(f"Discord rate limited after {max_retries} attempts")
                return False
            # Other HTTP errors (4xx, 5xx)
            logger.error(f"Discord HTTP error {e.code}: {e.reason}")
            return False
        except (urllib.error.URLError, socket.timeout, OSError) as e:
            # Network error - retry with backoff
            if attempt < max_retries - 1:
                time.sleep(backoff * (attempt + 1))
                continue
            logger.error(f"Discord error after {max_retries} attempts: {e}")
            return False
        except Exception as e:
            logger.error(f"Discord error: {e}")
            return False
    return False

def escape_telegram_markdown(text):
    """
    Escape special characters for Telegram MarkdownV2 format.
    This utility prevents formatting breaks when miner names or stats contain special characters.

    Characters that must be escaped: _ * [ ] ( ) ~ ` > # + - = | { } . !
    """
    if not text:
        return ""
    # All MarkdownV2 special characters
    special_chars = ['_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!']
    result = str(text)
    for char in special_chars:
        result = result.replace(char, f"\\{char}")
    return result


def embed_to_telegram_text(embed):
    """Convert Discord embed to Telegram-formatted message (MarkdownV2)"""
    lines = []

    # Title with emoji
    title = embed.get("title", "")
    if title:
        lines.append(f"*{escape_telegram_markdown(title)}*")

    # Description
    desc = embed.get("description", "")
    if desc:
        # Remove code blocks for cleaner telegram display
        desc = desc.replace("```diff\n", "").replace("```", "").replace("```ansi\n", "")
        lines.append(escape_telegram_markdown(desc))

    # Fields
    fields = embed.get("fields", [])
    for field in fields:
        name = field.get("name", "")
        value = field.get("value", "")
        if name:
            lines.append(f"\n*{escape_telegram_markdown(name)}*")
            lines.append(escape_telegram_markdown(value))

    # Footer
    footer = embed.get("footer", {}).get("text", "")
    if footer:
        lines.append(f"\n_{escape_telegram_markdown(footer)}_")

    return "\n".join(lines)


def embed_to_xmpp_text(embed):
    """Convert Discord embed to plain text for XMPP (no markdown formatting)."""
    lines = []

    title = embed.get("title", "")
    if title:
        lines.append(title)

    desc = embed.get("description", "")
    if desc:
        # Strip code block markers
        desc = desc.replace("```diff\n", "").replace("```", "").replace("```ansi\n", "")
        lines.append(desc)

    fields = embed.get("fields", [])
    for field in fields:
        name = field.get("name", "")
        value = field.get("value", "")
        if name:
            lines.append(f"\n{name}")
            lines.append(value)

    footer = embed.get("footer", {}).get("text", "")
    if footer:
        lines.append(f"\n— {footer}")

    return "\n".join(lines)


def send_telegram(embed):
    """Send notification to Telegram using Bot API with automatic retry on network failures."""
    if not TELEGRAM_ENABLED: return False

    # Audit #9: Rate limit Telegram sends to prevent burst flooding
    global _last_telegram_send
    now = time.monotonic()
    elapsed = now - _last_telegram_send
    if elapsed < _TELEGRAM_MIN_INTERVAL:
        time.sleep(_TELEGRAM_MIN_INTERVAL - elapsed)
    _last_telegram_send = time.monotonic()

    max_retries = 3
    backoff = 2  # seconds

    # Convert embed to Telegram message format
    text = embed_to_telegram_text(embed)

    # Telegram Bot API URL
    url = f"https://api.telegram.org/bot{TELEGRAM_BOT_TOKEN}/sendMessage"

    payload = {
        "chat_id": TELEGRAM_CHAT_ID,
        "text": text,
        "parse_mode": "MarkdownV2",
        "disable_web_page_preview": True
    }

    for attempt in range(max_retries):
        try:
            req = urllib.request.Request(url, data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=15) as resp:
                result = json.loads(resp.read().decode())
                return result.get("ok", False)
        except (urllib.error.URLError, socket.timeout, OSError) as e:
            # Network error - retry with backoff
            if attempt < max_retries - 1:
                time.sleep(backoff * (attempt + 1))
                continue
            # Sanitize error message to prevent bot token leakage in logs
            err_msg = str(e).replace(TELEGRAM_BOT_TOKEN, "***") if TELEGRAM_BOT_TOKEN else str(e)
            logger.error(f"Telegram error after {max_retries} attempts: {err_msg}")
            return False
        except Exception as e:
            err_msg = str(e).replace(TELEGRAM_BOT_TOKEN, "***") if TELEGRAM_BOT_TOKEN else str(e)
            logger.error(f"Telegram error: {err_msg}")
            return False
    return False


def send_xmpp(embed):
    """Send notification via XMPP using slixmpp.

    Uses asyncio.run() to bridge sync Sentinel code into slixmpp's async world.
    Each call creates a fresh ClientXMPP, connects, sends, and disconnects.

    API reference:
      - slixmpp send_client.py example: connect() then await disconnected
      - join_muc_wait() replaces deprecated join_muc() (removed in 1.9.0)
      - process() replaced by await disconnected (removed in 1.9.0)
    """
    if not XMPP_ENABLED:
        return False
    if not XMPP_AVAILABLE:
        logger.error("XMPP enabled but slixmpp not installed")
        return False

    text = embed_to_xmpp_text(embed)

    try:
        return asyncio.run(_xmpp_send(XMPP_JID, XMPP_PASSWORD, XMPP_RECIPIENT, text, XMPP_USE_TLS, XMPP_MUC))
    except Exception as e:
        logger.error(f"XMPP error: {e}")
        return False


async def _xmpp_send(jid, password, recipient, message, use_tls, is_muc):
    """Internal async XMPP send using slixmpp.

    Pattern mirrors slixmpp/examples/send_client.py:
      1. Create ClientXMPP, register plugins, add session_start handler
      2. connect() — initiates TCP/TLS connection
      3. await disconnected — drives event loop until disconnect()
      4. session_start handler: send_presence, get_roster, send_message, disconnect
    """
    xmpp = slixmpp.ClientXMPP(jid, password)
    xmpp.register_plugin('xep_0030')  # Service Discovery
    xmpp.register_plugin('xep_0199')  # Ping
    if is_muc:
        xmpp.register_plugin('xep_0045')  # MUC

    success = [False]

    async def on_session_start(event):
        xmpp.send_presence()
        await xmpp.get_roster()

        if is_muc:
            # join_muc_wait() replaced deprecated join_muc() in slixmpp 1.8+
            await xmpp.plugin['xep_0045'].join_muc_wait(recipient, 'SpiralSentinel')
            xmpp.send_message(mto=recipient, mbody=message, mtype='groupchat')
        else:
            xmpp.send_message(mto=recipient, mbody=message, mtype='chat')

        success[0] = True
        xmpp.disconnect()

    xmpp.add_event_handler("session_start", on_session_start)

    xmpp.connect(use_tls=use_tls)

    # Wait for disconnect (drives the event loop) with a timeout.
    # xmpp.disconnected is a Future that resolves when the stream closes.
    # This replaces the deprecated xmpp.process(forever=False).
    try:
        await asyncio.wait_for(xmpp.disconnected, timeout=15)
    except asyncio.TimeoutError:
        logger.error("XMPP send timed out after 15s")
        xmpp.disconnect()
        return False

    return success[0]


def send_ntfy(embed):
    """Send notification via ntfy (https://ntfy.sh or self-hosted).

    ntfy receives a plain-text message body with a title header.
    Auth token is optional — required only for private topics or self-hosted servers
    that have auth enabled.

    ntfy URL must be the full topic URL, e.g.:
      https://ntfy.sh/your_topic_name
      https://ntfy.yourserver.com/alerts
    """
    if not NTFY_ENABLED:
        return False

    # Read config fresh to pick up runtime changes
    current_config = load_config()
    ntfy_url   = current_config.get("ntfy_url", "").strip()
    ntfy_token = current_config.get("ntfy_token", "").strip()

    if not ntfy_url:
        return False

    # SECURITY: Only allow https:// URLs
    if not ntfy_url.startswith("https://"):
        logger.error("SECURITY: ntfy_url must use https://")
        return False

    # Build plain-text message body from embed
    title   = embed.get("title", "Spiral Pool Alert")
    desc    = embed.get("description", "")
    # Strip Discord markdown formatting (code blocks, bold, etc.) for plain-text channels
    import re as _re
    desc_plain = _re.sub(r"```[a-z]*\n?", "", desc).replace("```", "").strip()
    fields_lines = []
    for field in embed.get("fields", []):
        fname  = field.get("name", "")
        fvalue = field.get("value", "")
        # Strip markdown from field values too
        fvalue_plain = _re.sub(r"\*\*|`|\[([^\]]+)\]\([^\)]+\)", r"\1", fvalue).strip()
        if fname and fvalue_plain:
            fields_lines.append(f"{fname}: {fvalue_plain}")

    body_parts = []
    if desc_plain:
        body_parts.append(desc_plain[:500])  # ntfy body limit is 4096 but keep it readable
    if fields_lines:
        body_parts.append("\n".join(fields_lines[:8]))  # Max 8 fields in body
    body = "\n\n".join(body_parts) if body_parts else title

    # ntfy priority based on embed colour
    color = embed.get("color", 0)
    if color == COLORS.get("red", 0):
        priority = "high"
    elif color == COLORS.get("yellow", 0) or color == COLORS.get("orange", 0):
        priority = "default"
    else:
        priority = "default"

    headers = {
        "Title":    title[:250],   # ntfy title header limit
        "Priority": priority,
        "Content-Type": "text/plain",
    }
    if ntfy_token:
        headers["Authorization"] = f"Bearer {ntfy_token}"

    # Include explorer URL as an action button if present
    explorer_url = embed.get("url")
    if explorer_url:
        headers["Actions"] = f"view, View Block, {explorer_url}"

    max_retries = 3
    for attempt in range(max_retries):
        try:
            req = urllib.request.Request(
                ntfy_url,
                data=body.encode("utf-8"),
                headers=headers,
                method="POST"
            )
            with urllib.request.urlopen(req, timeout=15) as resp:
                if resp.status in [200, 201]:
                    logger.debug(f"ntfy notification sent (status {resp.status})")
                    return True
                else:
                    logger.warning(f"ntfy unexpected status: {resp.status}")
                    return False
        except (urllib.error.URLError, socket.timeout, OSError) as e:
            if attempt < max_retries - 1:
                time.sleep(2 * (attempt + 1))
                continue
            logger.error(f"ntfy error after {max_retries} attempts: {e}")
            return False
        except Exception as e:
            logger.error(f"ntfy error: {e}")
            return False
    return False


def _tg_send_text(text):
    """Send a plain-text reply to the configured Telegram chat (for bot command responses)."""
    if not TELEGRAM_ENABLED:
        return
    url = f"https://api.telegram.org/bot{TELEGRAM_BOT_TOKEN}/sendMessage"
    # Telegram hard limit is 4096 chars. Truncate at 4000 so there's room for the ellipsis
    # and to avoid cutting in the middle of a MarkdownV2 escape sequence (which causes
    # Telegram to reject the entire message with a 400 parse error).
    if len(text) > 4000:
        text = text[:4000] + "\n\\.\\.\\."   # MarkdownV2-escaped "..."
    payload = {
        "chat_id": TELEGRAM_CHAT_ID,
        "text": text,
        "parse_mode": "MarkdownV2",
        "disable_web_page_preview": True,
    }
    try:
        req = urllib.request.Request(
            url, data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"}
        )
        with urllib.request.urlopen(req, timeout=10):
            pass
    except Exception as e:
        err = str(e).replace(TELEGRAM_BOT_TOKEN, "***") if TELEGRAM_BOT_TOKEN else str(e)
        logger.debug(f"Telegram command reply error: {err}")


def _tg_escape(text):
    """Escape text for Telegram MarkdownV2."""
    for ch in r"\_*[]()~`>#+-=|{}.!":
        text = text.replace(ch, f"\\{ch}")
    return text


def _handle_telegram_command(cmd, state):
    """Dispatch a Telegram bot command and reply with pool status information."""
    raw_text = cmd.strip()  # preserve original for argument parsing in /pause
    # Extract just the command word: "/pause 30" → "/pause", "/status@MyBot" → "/status"
    cmd = raw_text.lower().split()[0].split("@")[0] if raw_text else ""

    pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")

    if cmd == "/help":
        lines = [
            "*Spiral Pool — Bot Commands*",
            "",
            "`/status` — Service \\+ node overview",
            "`/miners` — Connected miners table",
            "`/hashrate` — Pool hashrate \\+ difficulty",
            "`/blocks` — Last 5 blocks found",
            "`/uptime` — Sentinel \\+ stratum uptime",
            "`/pause [minutes]` — Pause alerts \\(default 30, max 1440 min\\)",
            "`/resume` — Resume alerts immediately",
            "`/cooldowns` — Show active alert cooldowns",
            "`/help` — This message",
        ]
        _tg_send_text("\n".join(lines))
        return

    if cmd == "/status":
        try:
            data = _http(f"{pool_url}/api/pools", timeout=8)
            pools = data if isinstance(data, list) else []
            lines = ["*Pool Status*", ""]
            for p in pools:
                symbol = _tg_escape(p.get("coin", {}).get("symbol", "?"))
                miners = p.get("poolStats", {}).get("connectedMiners", 0)
                hr_raw = p.get("poolStats", {}).get("poolHashrate", 0)
                hr_str = _tg_escape(format_hashrate(hr_raw) if hr_raw else "0 H/s")
                lines.append(f"*{symbol}* — {miners} miner\\(s\\) — {hr_str}")
            if not lines[2:]:
                lines.append("No active pools")
            # Show pause/maintenance status if active
            if PAUSE_FILE.exists():
                try:
                    with open(PAUSE_FILE) as _pf:
                        _pd = json.load(_pf)
                    _pu = _pd.get("pause_until", 0)
                    _pr = _pd.get("reason", "")
                    _rem = _pu - time.time()
                    if _rem > 0:
                        _mins = int(_rem / 60) + 1
                        lines.append("")
                        lines.append(_tg_escape(f"⏸ Alerts paused — {_mins}m remaining"))
                        if _pr:
                            lines.append(_tg_escape(f"Reason: {_pr}"))
                except Exception:
                    pass
            _tg_send_text("\n".join(lines))
        except Exception as e:
            _tg_send_text(_tg_escape(f"Error fetching status: {e}"))
        return

    if cmd == "/hashrate":
        try:
            data = _http(f"{pool_url}/api/pools", timeout=8)
            pools = data if isinstance(data, list) else []
            lines = ["*Hashrate \\& Difficulty*", ""]
            for p in pools:
                symbol = _tg_escape(p.get("coin", {}).get("symbol", "?"))
                hr_raw = p.get("poolStats", {}).get("poolHashrate", 0)
                hr_str = _tg_escape(format_hashrate(hr_raw) if hr_raw else "0 H/s")
                net_diff = p.get("networkStats", {}).get("networkDifficulty", 0)
                diff_str = _tg_escape(f"{net_diff:,.0f}" if net_diff else "?")
                lines.append(f"*{symbol}*: {hr_str} \\| Diff: {diff_str}")
            if not lines[2:]:
                lines.append("No data")
            _tg_send_text("\n".join(lines))
        except Exception as e:
            _tg_send_text(_tg_escape(f"Error: {e}"))
        return

    if cmd == "/miners":
        try:
            data = _http(f"{pool_url}/api/pools", timeout=8)
            pools = data if isinstance(data, list) else []
            lines = ["*Connected Miners*", ""]
            found_any = False
            nicknames = CONFIG.get("miner_nicknames", {})
            for p in pools:
                pool_id = p.get("id", "")
                if not pool_id:
                    continue
                try:
                    miners_data = _http(
                        f"{pool_url}/api/pools/{url_quote(pool_id)}/miners", timeout=8
                    )
                    miners = miners_data if isinstance(miners_data, list) else []
                    symbol = _tg_escape(p.get("coin", {}).get("symbol", "?"))
                    for m in miners[:10]:  # Cap at 10 per coin to avoid truncation
                        full_addr = str(m.get("address", "?"))
                        nick = nicknames.get(full_addr)
                        if nick:
                            label = _tg_escape(nick)
                        else:
                            label = f"`{_tg_escape(full_addr[:12])}…`"
                        hr_raw = m.get("hashrate", 0)
                        hr_str = _tg_escape(format_hashrate(hr_raw) if hr_raw else "0 H/s")
                        shares = m.get("sharesPerSecond", 0)
                        lines.append(f"*{symbol}* {label} — {hr_str} \\({shares:.2f} sh/s\\)")
                        found_any = True
                except Exception:
                    pass
            if not found_any:
                lines.append("No miners connected")
            _tg_send_text("\n".join(lines))
        except Exception as e:
            _tg_send_text(_tg_escape(f"Error: {e}"))
        return

    if cmd == "/blocks":
        try:
            data = _http(f"{pool_url}/api/pools", timeout=8)
            pools = data if isinstance(data, list) else []
            lines = ["*Last Blocks Found*", ""]
            found_any = False
            for p in pools:
                pool_id = p.get("id", "")
                if not pool_id:
                    continue
                try:
                    blocks_data = _http(
                        f"{pool_url}/api/pools/{url_quote(pool_id)}/blocks?pageSize=5", timeout=8
                    )
                    blocks = blocks_data if isinstance(blocks_data, list) else []
                    symbol = _tg_escape(p.get("coin", {}).get("symbol", "?"))
                    for b in blocks:
                        height = _tg_escape(str(b.get("blockHeight", "?")))
                        reward = b.get("reward", 0)
                        reward_str = _tg_escape(f"{reward:.4f}" if reward else "?")
                        ts = b.get("created", "")
                        ts_str = _tg_escape(ts[:10] if ts else "?")
                        lines.append(f"*{symbol}* Block {height} — {reward_str} — {ts_str}")
                        found_any = True
                except Exception:
                    pass
            if not found_any:
                lines.append("No blocks found yet")
            _tg_send_text("\n".join(lines))
        except Exception as e:
            _tg_send_text(_tg_escape(f"Error: {e}"))
        return

    if cmd == "/uptime":
        try:
            def _tg_fmt_uptime(secs):
                secs = max(0, int(secs))
                d, r = divmod(secs, 86400)
                h, r = divmod(r, 3600)
                m = r // 60
                if d:   return f"{d}d {h}h {m}m"
                if h:   return f"{h}h {m}m"
                return f"{m}m"

            lines = ["*Uptime*", ""]
            lines.append(f"Sentinel: `{_tg_fmt_uptime(time.time() - SENTINEL_START_TIME)}`")

            # Stratum uptime from systemd
            try:
                import subprocess as _sp, datetime as _dt2
                _r = _sp.run(
                    ["systemctl", "show", "spiralstratum.service", "--property=ActiveEnterTimestamp"],
                    capture_output=True, text=True, timeout=3
                )
                ts_line = _r.stdout.strip()
                if _r.returncode == 0 and "=" in ts_line:
                    ts_val = ts_line.split("=", 1)[1].strip()
                    if ts_val and ts_val.lower() not in ("n/a", ""):
                        parts = ts_val.split()
                        if len(parts) >= 3:
                            ts_str = " ".join(parts[1:])
                            if ts_str.endswith(" UTC"):
                                dt = _dt2.datetime.strptime(ts_str, "%Y-%m-%d %H:%M:%S UTC")
                                dt = dt.replace(tzinfo=_dt2.timezone.utc)
                                stratum_secs = (datetime.now(timezone.utc) - dt).total_seconds()
                            else:
                                dt = _dt2.datetime.strptime(ts_str.rsplit(" ", 1)[0], "%Y-%m-%d %H:%M:%S")
                                stratum_secs = time.time() - dt.timestamp()
                            if stratum_secs > 0:
                                lines.append(f"Stratum: `{_tg_fmt_uptime(stratum_secs)}`")
            except Exception:
                pass  # systemd unavailable — skip stratum line

            _tg_send_text("\n".join(lines))
        except Exception as e:
            _tg_send_text(_tg_escape(f"Error: {e}"))
        return

    if cmd == "/pause":
        try:
            # /pause [minutes] — default 30; use raw_text to get the argument
            parts = raw_text.strip().split()
            minutes = 30
            if len(parts) >= 2:
                try:
                    minutes = max(1, min(int(parts[1]), 1440))  # 1 min – 24 hours
                except ValueError:
                    _tg_send_text(_tg_escape("Usage: /pause [minutes] (default: 30, max: 1440)"))
                    return
            pause_until = time.time() + minutes * 60
            try:
                with open(PAUSE_FILE, "w") as f:
                    json.dump({"pause_until": pause_until, "reason": "Paused via Telegram bot"}, f)
                _tg_send_text(_tg_escape(f"Alerts paused for {minutes} minute{'s' if minutes != 1 else ''}. Use /resume to cancel early."))
            except OSError as e:
                _tg_send_text(_tg_escape(f"Failed to write pause file: {e}"))
        except Exception as e:
            _tg_send_text(_tg_escape(f"Error: {e}"))
        return

    if cmd == "/resume":
        try:
            if PAUSE_FILE.exists():
                PAUSE_FILE.unlink(missing_ok=True)
                _tg_send_text(_tg_escape("Alerts resumed."))
            else:
                _tg_send_text(_tg_escape("Alerts are not currently paused."))
        except Exception as e:
            _tg_send_text(_tg_escape(f"Error: {e}"))
        return

    if cmd == "/cooldowns":
        try:
            health_port = CONFIG.get("sentinel_health_port", 9191)
            cd_data = _http(f"http://127.0.0.1:{health_port}/cooldowns", timeout=3)
            active = cd_data if isinstance(cd_data, list) else []
            if not active:
                _tg_send_text(_tg_escape("No active alert cooldowns."))
            else:
                lines = ["*Active Alert Cooldowns*", ""]
                for entry in active[:15]:  # cap to avoid message truncation
                    atype = _tg_escape(entry.get("type", "?"))
                    mins = int(entry.get("expires_in_s", 0) / 60) + 1
                    lines.append(f"`{atype}` — {mins}m remaining")
                if len(active) > 15:
                    lines.append(_tg_escape(f"…and {len(active) - 15} more"))
                _tg_send_text("\n".join(lines))
        except Exception as e:
            _tg_send_text(_tg_escape(f"Error fetching cooldowns: {e}"))
        return

    # Unknown command — send help hint
    _tg_send_text(_tg_escape("Unknown command. Send /help for available commands."))


def _telegram_command_loop(state):
    """Background thread: polls Telegram getUpdates and dispatches bot commands.

    Only responds to messages from the configured telegram_chat_id — all other
    chat IDs are silently ignored to prevent unauthorized access.
    Long-polls with timeout=25s; reconnects on error with 5s backoff.
    Rate-limited to one command per 3 seconds to prevent abuse.
    """
    global _telegram_cmd_offset, _tg_last_cmd_ts
    poll_url = f"https://api.telegram.org/bot{TELEGRAM_BOT_TOKEN}/getUpdates"

    while True:
        try:
            params = f"?timeout=25&offset={_telegram_cmd_offset}&allowed_updates=message"
            req = urllib.request.Request(
                poll_url + params,
                headers={"Content-Type": "application/json"}
            )
            with urllib.request.urlopen(req, timeout=35) as resp:
                data = json.loads(resp.read().decode())

            if not data.get("ok"):
                time.sleep(5)
                continue

            for update in data.get("result", []):
                _telegram_cmd_offset = update["update_id"] + 1
                msg = update.get("message", {})
                chat_id = str(msg.get("chat", {}).get("id", ""))
                # SECURITY: only respond to the configured chat
                if chat_id != str(TELEGRAM_CHAT_ID):
                    continue
                text = msg.get("text", "")
                if text.startswith("/"):
                    # Rate limit: one command per 3 seconds
                    now = time.time()
                    if now - _tg_last_cmd_ts < 3.0:
                        logger.debug("Telegram command rate-limited, dropping")
                        continue
                    _tg_last_cmd_ts = now
                    try:
                        _handle_telegram_command(text, state)
                    except Exception as e:
                        logger.debug(f"Telegram command handler error: {e}")

        except (urllib.error.URLError, socket.timeout, OSError):
            time.sleep(5)
        except Exception as e:
            err = str(e).replace(TELEGRAM_BOT_TOKEN, "***") if TELEGRAM_BOT_TOKEN else str(e)
            logger.debug(f"Telegram poll error: {err}")
            time.sleep(5)


class _HealthHandler(http.server.BaseHTTPRequestHandler):
    """Minimal HTTP handler for the Sentinel health endpoint (loopback only).

    GET /health   → {"alive": true, "uptime_s": N, "version": "<__version__>"}
    GET /cooldowns → list of active alert cooldowns with time remaining, sorted by expiry
    """

    def log_message(self, format, *args):
        pass  # Suppress access logs — already in journald

    def do_GET(self):
        if self.path == "/health":
            self._send_health()
        elif self.path == "/cooldowns":
            self._send_cooldowns()
        else:
            self.send_error(404)

    def _write_json(self, data):
        body = json.dumps(data, separators=(",", ":")).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_health(self):
        self._write_json({
            "alive": True,
            "uptime_s": int(time.time() - SENTINEL_START_TIME),
            "version": __version__,
        })

    def _send_cooldowns(self):
        state = _health_state_ref
        if state is None or not hasattr(state, "last_alerts"):
            self._write_json([])
            return

        # Snapshot the dict before iterating — the monitor loop modifies last_alerts
        # from its own thread and Python's GIL does NOT prevent RuntimeError from
        # "dictionary changed size during iteration" on .items() view objects.
        alerts_snapshot = dict(state.last_alerts)
        now = time.time()
        active = []
        for alert_type, last_sent in alerts_snapshot.items():
            cooldown = ALERT_COOLDOWNS.get(alert_type, 0)
            if cooldown <= 0:
                continue
            remaining = (last_sent + cooldown) - now
            if remaining > 0:
                active.append({
                    "type": alert_type,
                    "expires_in_s": int(remaining),
                    "cooldown_s": cooldown,
                    "last_sent": time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(last_sent)),
                })
        active.sort(key=lambda x: x["expires_in_s"], reverse=True)
        self._write_json(active)


def _health_server_loop(port):
    """Background thread: runs the Sentinel health HTTP server on loopback.

    Retries on error so a transient port conflict (e.g., old process still dying)
    doesn't permanently take down the health endpoint.
    """
    import socketserver as _ss

    class _ReuseServer(_ss.TCPServer):
        allow_reuse_address = True

    while True:
        try:
            with _ReuseServer(("127.0.0.1", port), _HealthHandler) as httpd:
                logger.info(f"Sentinel health endpoint: http://127.0.0.1:{port}/health")
                httpd.serve_forever()
        except OSError as e:
            logger.warning(f"Sentinel health endpoint error on port {port}: {e} — retrying in 30s")
        except Exception as e:
            logger.warning(f"Sentinel health endpoint crashed: {e} — retrying in 30s")
        time.sleep(30)


def send_email(embed):
    """Send notification via SMTP email.

    Uses STARTTLS (port 587, recommended) or SSL (port 465) depending on
    smtp_use_tls config. Credentials are stored in config.json which is
    chmod 600 owned by spiraluser — same hardening as Discord webhook and
    Telegram bot token.
    """
    if not SMTP_ENABLED:
        return False

    import smtplib
    from email.mime.multipart import MIMEMultipart
    from email.mime.text import MIMEText

    # Build subject from embed title
    title = embed.get("title", "Spiral Pool Alert")
    # Strip Discord emoji/markdown from title for clean subject line
    subject = re.sub(r"[*_`~]", "", title).strip()

    # Build plain-text body
    lines = [subject, ""]
    desc = embed.get("description", "")
    if desc:
        lines.append(re.sub(r"\*\*(.+?)\*\*", r"\1", desc))
        lines.append("")
    for field in embed.get("fields", []):
        name  = re.sub(r"[*_`]", "", field.get("name", "")).strip()
        value = re.sub(r"\[(.+?)\]\(.+?\)", r"\1",
                       re.sub(r"[*_`]", "", field.get("value", ""))).strip()
        if name and value:
            lines.append(f"{name}: {value}")
    explorer_url = embed.get("url")
    if explorer_url:
        lines.append("")
        lines.append(f"Block explorer: {explorer_url}")
    lines.append("")
    lines.append("— Spiral Pool Sentinel")

    body_plain = "\n".join(lines)

    msg = MIMEMultipart("alternative")
    msg["Subject"] = subject
    msg["From"]    = SMTP_FROM
    msg["To"]      = ", ".join(SMTP_TO)
    msg.attach(MIMEText(body_plain, "plain"))

    import ssl as _ssl
    _tls_context = _ssl.create_default_context()  # Verifies cert chain + hostname

    max_retries = 3
    for attempt in range(max_retries):
        try:
            if SMTP_USE_TLS:
                # STARTTLS on port 587 (recommended) — upgrades plain connection to TLS
                with smtplib.SMTP(SMTP_HOST, SMTP_PORT, timeout=15) as s:
                    s.ehlo()
                    s.starttls(context=_tls_context)  # Cert-verified TLS upgrade
                    s.ehlo()
                    s.login(SMTP_USERNAME, SMTP_PASSWORD)
                    s.sendmail(SMTP_FROM, SMTP_TO, msg.as_string())
            else:
                # SSL on port 465 — TLS from the first byte
                with smtplib.SMTP_SSL(SMTP_HOST, SMTP_PORT, timeout=15, context=_tls_context) as s:
                    s.login(SMTP_USERNAME, SMTP_PASSWORD)
                    s.sendmail(SMTP_FROM, SMTP_TO, msg.as_string())
            return True
        except smtplib.SMTPAuthenticationError as e:
            logger.error(f"SMTP authentication failed: {e}")
            return False  # No point retrying auth failures
        except (smtplib.SMTPException, OSError) as e:
            if attempt < max_retries - 1:
                time.sleep(2 * (attempt + 1))
                continue
            logger.error(f"SMTP error after {max_retries} attempts: {e}")
            return False
        except Exception as e:
            logger.error(f"SMTP error: {e}")
            return False
    return False


def send_notifications(embed):
    """Send notification to all configured channels (Discord, Telegram, XMPP, ntfy, and/or email)

    If all remote notifications fail on first attempt, retries once after a brief
    delay to handle transient network blips. Falls back to logging the alert
    to syslog and a local file for later review.
    """
    discord_sent  = send_discord(embed)
    telegram_sent = send_telegram(embed)
    xmpp_sent     = send_xmpp(embed)
    ntfy_sent     = send_ntfy(embed)
    email_sent    = send_email(embed)

    # Retry once if all channels failed (transient network blips)
    if not discord_sent and not telegram_sent and not xmpp_sent and not ntfy_sent and not email_sent:
        config = load_config()
        any_configured = (
            bool(config.get("discord_webhook_url")) or
            bool(config.get("telegram_bot_token") and config.get("telegram_chat_id")) or
            bool(config.get("xmpp_jid") and config.get("xmpp_recipient")) or
            bool(config.get("ntfy_url")) or
            bool(config.get("smtp_host") and config.get("smtp_to"))
        )
        if any_configured:
            logger.warning("All notification channels failed — retrying in 10s...")
            time.sleep(10)
            discord_sent  = send_discord(embed)
            telegram_sent = send_telegram(embed)
            xmpp_sent     = send_xmpp(embed)
            ntfy_sent     = send_ntfy(embed)
            email_sent    = send_email(embed)

    # If all remote notifications failed, log locally as fallback
    if not discord_sent and not telegram_sent and not xmpp_sent and not ntfy_sent and not email_sent:
        # Only log fallback if at least one channel was configured
        config = load_config()
        discord_configured  = bool(config.get("discord_webhook_url"))
        telegram_configured = bool(config.get("telegram_bot_token") and config.get("telegram_chat_id"))
        xmpp_configured     = bool(config.get("xmpp_jid") and config.get("xmpp_recipient"))
        ntfy_configured     = bool(config.get("ntfy_url"))
        email_configured    = bool(config.get("smtp_host") and config.get("smtp_to"))

        if discord_configured or telegram_configured or xmpp_configured or ntfy_configured or email_configured:
            # Convert embed to plain text for logging
            title = embed.get("title", "Alert")
            desc = embed.get("description", "")
            fields_text = ""
            for field in embed.get("fields", []):
                fields_text += f" | {field.get('name', '')}: {field.get('value', '')}"

            fallback_msg = f"[NOTIFICATION FALLBACK] {title}: {desc}{fields_text}"

            # Log to application log
            logger.warning(fallback_msg)

            # Also write to dedicated fallback file (with size limit to prevent disk exhaustion)
            try:
                fallback_file = str(DATA_DIR / "fallback_notifications.log")
                os.makedirs(os.path.dirname(fallback_file), exist_ok=True)
                # Rotate if file exceeds 5MB
                if os.path.exists(fallback_file) and os.path.getsize(fallback_file) > 5 * 1024 * 1024:
                    rotated = fallback_file + ".1"
                    if os.path.exists(rotated):
                        os.remove(rotated)
                    os.rename(fallback_file, rotated)
                with open(fallback_file, "a") as f:
                    timestamp = local_now().strftime("%Y-%m-%d %H:%M:%S")
                    f.write(f"[{timestamp}] {fallback_msg}\n")
            except Exception as e:
                logger.error(f"Failed to write fallback notification: {e}")

    return discord_sent or telegram_sent or xmpp_sent or ntfy_sent or email_sent

def kick_stratum_session(ip):
    """Kick all stratum sessions from the given IP via the pool admin API.

    Returns the number of sessions closed, or 0 on failure (no admin key,
    API unavailable, or no active sessions for that IP).
    """
    pool_url = CONFIG.get("pool_api_url", "http://localhost:4000")
    admin_key = CONFIG.get("pool_admin_api_key", "")
    if not admin_key or not ip:
        return 0
    try:
        url = f"{pool_url}/api/admin/kick?ip={url_quote(ip)}"
        req = urllib.request.Request(url, method="POST", headers={"X-API-Key": admin_key})
        with urllib.request.urlopen(req, timeout=8) as resp:
            data = json.loads(resp.read().decode())
            return int(data.get("kicked", 0))
    except Exception as e:
        logger.debug(f"Stratum kick failed for {ip}: {e}")
        return 0


def is_in_startup_suppression():
    """Check if we're in the startup alert suppression window.

    During startup, services may be initializing and nodes may not be fully
    communicating. This window suppresses non-critical alerts to prevent
    alert spam during restarts/reboots.

    Returns:
        tuple: (is_suppressed, minutes_remaining)
    """
    global SENTINEL_STARTUP_TIME
    if SENTINEL_STARTUP_TIME is None:
        return False, 0

    elapsed = time.time() - SENTINEL_STARTUP_TIME
    suppression_seconds = STARTUP_ALERT_SUPPRESSION_MINUTES * 60

    if elapsed < suppression_seconds:
        remaining = (suppression_seconds - elapsed) / 60
        return True, remaining

    return False, 0


# ═══════════════════════════════════════════════════════════════════════════════
# BLOCK FOUND CELEBRATION - Avalon LED Flash
# ═══════════════════════════════════════════════════════════════════════════════
# Path to the celebration script (Linux only)
CELEBRATION_SCRIPT = str(INSTALL_DIR / "scripts" / "block-celebrate.sh")

def trigger_block_celebration(miner_details=None):
    """
    Trigger LED celebration on Avalon miners when a block is found.

    This runs the block-celebrate.sh script in the background, which:
    - Discovers all CGMiner-compatible miners on the network
    - Saves their current LED state
    - Runs an epic LED celebration (duration from config.yaml, default 2 hours)
    - Restores original LED state when done

    Note: LED celebration is suppressed during quiet hours to avoid
    waking people up. The block found alert still fires (bypasses quiet hours).

    Args:
        miner_details: Dict of miner info (optional, used for logging)
    """
    import subprocess
    import platform

    # Suppress LED celebration during quiet hours (alert still fires)
    if is_quiet_hours():
        logger.info("Block celebration: Skipped during quiet hours (LEDs would wake people up)")
        return

    # Only runs on Linux - Windows doesn't have the bash script
    if platform.system() != "Linux":
        logger.debug("Block celebration: Skipped (not Linux)")
        return

    # Check if script exists
    if not os.path.exists(CELEBRATION_SCRIPT):
        logger.warning(f"Block celebration: Script not found at {CELEBRATION_SCRIPT}")
        return

    try:
        # Read celebration duration from stratum config.yaml
        duration_seconds = 10800  # Default 3 hours
        try:
            config_yaml = INSTALL_DIR / "config" / "config.yaml"
            if config_yaml.exists():
                with open(config_yaml) as f:
                    for line in f:
                        line = line.strip()
                        if line.startswith("duration_hours:"):
                            hours = float(line.split(":", 1)[1].strip())
                            duration_seconds = int(hours * 3600)
                            break
        except Exception:
            pass  # Fall back to default

        # Run celebration in background (non-blocking)
        # The script handles its own discovery and cleanup
        logger.info(f"🎉 BLOCK CELEBRATION: Triggering Avalon LED celebration! ({duration_seconds // 3600}h)")

        # Start the celebration script in background
        subprocess.Popen(
            [CELEBRATION_SCRIPT, "--duration", str(duration_seconds)],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True  # Fully detach from Sentinel process
        )

        if miner_details:
            cgminer_count = sum(1 for m in miner_details.values()
                               if m.get("online", False))
            logger.info(f"Block celebration: Started for {cgminer_count} online miners")

    except Exception as e:
        # Don't let celebration failure affect the main alert flow
        logger.error(f"Block celebration failed: {e}")


def send_alert(alert_type, embed, state=None, miner_name=None):
    """Send alert with HA coordination, rate limiting, quiet hours, maintenance mode, startup suppression, batching, and blockchain sync checks.

    HA coordination ensures only the MASTER Sentinel sends alerts in multi-node setups.
    Blockchain sync check prevents false alerts during initial node sync.
    Startup suppression prevents alert spam during service initialization after restart.
    Rate limiting prevents Discord webhook spam.
    Master ALERTS_ENABLED toggle can disable all alerts globally.
    Maintenance mode pauses all alerts for a specified duration.
    Alert batching combines multiple miner alerts into digest notifications.

    Args:
        alert_type: Type of alert (e.g., "miner_offline", "block_found")
        embed: Discord embed dict
        state: MonitorState instance (optional, used for rate limiting and batching)
        miner_name: Name of miner for miner-specific alerts (optional, used for batching)
    """
    # Master alerts toggle - if disabled, skip all alerts except reports and critical events
    # block_found ALWAYS goes through - you always want to know when you find a block!
    if not ALERTS_ENABLED and alert_type not in ["6h_report", "weekly_report", "monthly_earnings", "startup_summary", "block_found"]:
        return False

    # STARTUP SUPPRESSION: During the startup window, suppress alerts except configured bypass types
    # This prevents alert spam when services are starting up after a restart/reboot
    # SECURITY: Bypass list is configurable to prevent blind spots during restarts
    suppressed, mins_remaining = is_in_startup_suppression()
    startup_bypass_list = CONFIG.get("startup_suppression_bypass", [
        "block_found", "startup_summary", "temp_critical",
        "6h_report", "weekly_report", "monthly_earnings", "quarterly_report"  # Scheduled reports should not be missed
    ])
    if suppressed and alert_type not in startup_bypass_list:
        # Log suppression occasionally (not every call to avoid spam)
        if state and hasattr(state, '_last_startup_suppress_log'):
            if time.time() - state._last_startup_suppress_log >= 60:  # Log every minute max
                logger.debug(f"Alert suppressed during startup ({mins_remaining:.1f} min remaining): {alert_type}")
                state._last_startup_suppress_log = time.time()
        elif state:
            logger.debug(f"Alert suppressed during startup ({mins_remaining:.1f} min remaining): {alert_type}")
            state._last_startup_suppress_log = time.time()
        return False

    # Scheduled reports that should NEVER be blocked by operational checks
    # These are time-sensitive and missing them defeats their purpose
    SCHEDULED_REPORT_TYPES = ["6h_report", "weekly_report", "monthly_earnings", "quarterly_report", "maintenance_reminder"]

    # HA COORDINATION: In HA mode, only master Sentinel sends alerts
    # This prevents triple-alerting when 3 nodes each run their own Sentinel
    # Block found alerts are ALWAYS sent (all nodes should celebrate!)
    # Startup summary is per-node (each Sentinel reports its own status)
    # Scheduled reports should still only come from master (not duplicated)
    if not is_master_sentinel() and alert_type not in ["block_found", "startup_summary", "ha_demoted"]:
        # Log suppression for debugging
        role = get_ha_role()
        if role != "STANDALONE":
            # Only log occasionally to avoid spam
            if state and hasattr(state, '_last_ha_suppress_log'):
                if time.time() - state._last_ha_suppress_log < 300:  # Log every 5 min max
                    pass
                else:
                    logger.debug(f"Alert suppressed (HA role: {role}, master sends alerts)")
                    state._last_ha_suppress_log = time.time()
            else:
                if state:
                    state._last_ha_suppress_log = time.time()
        return False

    # Maintenance mode - check both local and cluster-wide maintenance
    # Block found and scheduled reports still go through during maintenance
    paused, mins_left, reason, source = check_ha_maintenance_propagation()
    if paused and alert_type not in ["block_found", "startup_summary"] + SCHEDULED_REPORT_TYPES:
        return False

    # Skip all notifications if blockchain not synced (except startup and scheduled reports)
    # Scheduled reports should always fire on schedule - they contain cached data anyway
    if alert_type not in ["startup_summary", "block_found"] + SCHEDULED_REPORT_TYPES and not is_blockchain_ready():
        logger.debug(f"Alert blocked by blockchain not ready: {alert_type}")
        return False

    bypass = ALERT_BYPASS_QUIET.get(alert_type, False)
    if is_quiet_hours() and not bypass:
        logger.debug(f"Alert blocked by quiet hours: {alert_type}")
        return False

    # Scheduled maintenance windows — suppress alerts during planned downtime
    # Same bypass rules as quiet hours: scheduled reports and block_found always go through
    in_maint_window, maint_window_reason = is_in_maintenance_window()
    if in_maint_window and not bypass and alert_type not in SCHEDULED_REPORT_TYPES + ["block_found"]:
        logger.debug(f"Alert blocked by scheduled maintenance window ({maint_window_reason}): {alert_type}")
        return False

    if state and alert_type in ALERT_COOLDOWNS:
        last = state.last_alerts.get(alert_type, 0)
        if (time.time() - last) < ALERT_COOLDOWNS[alert_type]:
            logger.debug(f"Alert rate-limited: {alert_type}")
            return False
        state.last_alerts[alert_type] = time.time()

    # ═══════════════════════════════════════════════════════════════════════════════
    # ALERT BATCHING - Combine multiple miner alerts to reduce Discord spam
    # ═══════════════════════════════════════════════════════════════════════════════
    # If batching is enabled and this is a batchable alert type, queue it instead
    # of sending immediately. The batch will be flushed after the window expires.
    if ALERT_BATCHING_ENABLED and state and alert_type not in IMMEDIATE_ALERT_TYPES:
        state.queue_alert_for_batching(alert_type, embed, miner_name)
        logger.debug(f"Alert queued for batching: {alert_type} ({miner_name or 'fleet'})")
        return True  # Queued successfully

    # Send immediately for non-batchable alerts or when batching is disabled
    logger.info(f"Sending alert: {alert_type}")
    return send_notifications(embed)

def _embed(title, desc, color, fields=None, footer=None):
    # Discord embed limits: title 256, description 4096, footer 2048,
    # field name 256, field value 1024, 25 fields max, 6000 total chars.
    # Truncate to prevent silent 400 errors from Discord API.
    if title and len(title) > 256:
        title = title[:253] + "..."
    if desc and len(desc) > 4096:
        desc = desc[:4093] + "..."
    footer_text = footer or get_quote()
    # Append hostname so users can identify which server sent the alert
    if _SENTINEL_HOSTNAME:
        footer_text = f"{footer_text} • {_SENTINEL_HOSTNAME}"
    if footer_text and len(footer_text) > 2048:
        footer_text = footer_text[:2045] + "..."
    if fields and len(fields) > 25:
        fields = fields[:25]
    if fields:
        for f in fields:
            if f.get("name") and len(f["name"]) > 256:
                f["name"] = f["name"][:253] + "..."
            if f.get("value") and len(f["value"]) > 1024:
                f["value"] = f["value"][:1021] + "..."
    e = {"title": title, "description": desc, "color": color, "timestamp": utc_ts(), "footer": {"text": footer_text}}
    if fields: e["fields"] = fields
    return e

# === EMBED CREATORS ===

def create_stratum_down_embed(down_since):
    """Red alert embed for pool API going unreachable."""
    import datetime as _dt
    ts = _dt.datetime.fromtimestamp(down_since).strftime("%H:%M:%S")
    return _embed(
        "🔴 Pool Offline — Stratum Unreachable",
        "The pool API has stopped responding. Mining may be interrupted.",
        0xFF0000,  # Red
        fields=[
            {"name": "⏱️ Detected At", "value": f"`{ts}`", "inline": True},
            {"name": "🔍 Check", "value": "`sudo spiralctl status`", "inline": True},
        ],
    )


def create_stratum_recovered_embed(down_since, recovered_at):
    """Green recovery embed when pool API comes back online."""
    import datetime as _dt
    outage_secs = int(recovered_at - down_since)
    if outage_secs >= 3600:
        dur_str = f"{outage_secs // 3600}h {(outage_secs % 3600) // 60}m"
    elif outage_secs >= 60:
        dur_str = f"{outage_secs // 60}m {outage_secs % 60}s"
    else:
        dur_str = f"{outage_secs}s"
    return _embed(
        "🟢 Pool Back Online",
        "The pool API is responding again. Monitoring resumed.",
        0x00CC44,  # Green
        fields=[{"name": "⏱️ Outage Duration", "value": f"`{dur_str}`", "inline": True}],
    )


def create_config_warning_embed(issues):
    """Yellow warning embed listing config validation issues found at startup."""
    desc = ("The following configuration issues were detected at startup. "
            "Some alerts or features may not work correctly until these are resolved.\n\n"
            + "\n".join(f"• {issue}" for issue in issues))
    return _embed(
        "⚠️ Configuration Warning",
        desc,
        0xFFCC00,  # Yellow
        fields=[{"name": "🔧 Action Required", "value": "Edit `~spiraluser/.spiralsentinel/config.json` (or `/spiralpool/config/sentinel/config.json`) and restart Sentinel.", "inline": False}],
    )


def create_startup_embed(fleet_ths, md, temps, status, net_phs=None, odds=None, coin_symbol=None, power=None):
    """Enhanced startup embed with rich formatting and visual hierarchy.

    Supports algorithm-aware hashrate formatting for both SHA-256d and Scrypt coins.
    Shows all enabled coins when in multi-coin/merge-mining mode.
    """
    online = sum(1 for s in status.values() if s not in ("offline",))
    pool_only_count = sum(1 for s in status.values() if s == "pool_only")
    total = len(status)
    coin = coin_symbol.upper() if coin_symbol else (get_primary_coin() or "UNKNOWN")
    coin_emoji = get_coin_emoji(coin)
    coin_name = get_coin_name(coin)
    algorithm = get_coin_algorithm(coin)
    algo_label = "SCRYPT" if algorithm == "scrypt" else "SHA-256D"
    power = power or {}

    # Get all enabled coins to show in startup (multi-coin/merge-mining support)
    enabled_coins = get_enabled_coins()
    all_coin_symbols = [c.get("symbol", "?").upper() for c in enabled_coins] if enabled_coins else [coin] if coin else []

    # Sort miners by IP address for consistent ordering
    # Try to extract IP from miner name/key for natural numeric sorting
    def sort_key(item):
        name = item[0]
        # Try to parse as IP address for numeric sorting
        ip_match = re.search(r'(\d+)\.(\d+)\.(\d+)\.(\d+)', name)
        if ip_match:
            return tuple(int(x) for x in ip_match.groups())
        return (999, 999, 999, name)  # Non-IP names sort last

    sorted_miners = sorted(md.items(), key=sort_key)

    # Build miner list with power info
    miner_lines = []
    total_power = 0
    for n, hr in sorted_miners:
        is_pool_only = status.get(n) == "pool_only"
        is_online = status.get(n) not in ("offline",)
        icon = "🎰" if is_pool_only else ("🟢" if is_online else "🔴")
        display_name = get_miner_display_name(n)

        # Get power from power dict
        miner_power = power.get(n, 0)
        power_str = f"{miner_power:.0f}W" if miner_power > 0 else ""
        if miner_power > 0:
            total_power += miner_power

        line = f"{icon} **{display_name}**\n    └ `{hr}`" + (f" ⚡ `{power_str}`" if power_str else "")
        miner_lines.append(line)

    fields = []

    # Build mining description based on enabled coins and merge mining relationships
    # Distinguish between actual merge mining (AuxPoW) vs multi-coin pool switching
    is_merging, merge_summary, aux_coins = get_merge_mining_summary(coin)

    if is_merging and aux_coins:
        # Actual merge mining: parent chain mining aux chains via AuxPoW
        coins_display = f"**{coin}** ({algo_label})"
        coins_display += f"\n{merge_summary}"
        mining_line = f"💎 Mining {coins_display}"
    elif len(all_coin_symbols) > 1:
        # Multi-coin pool mode: separate pools, not merge mining
        other_coins = [c for c in all_coin_symbols if c != coin]
        coins_display = f"**{coin}** ({algo_label})"
        if other_coins:
            # Categorize other coins: merge-mineable vs separate pools
            other_merge = [c for c in other_coins if c in aux_coins or is_aux_chain(c)]
            other_separate = [c for c in other_coins if c not in other_merge]
            if other_merge:
                merge_str = ", ".join([f"{get_coin_emoji(c)} {c}" for c in other_merge])
                coins_display += f"\n🔗 Merge-mining: {merge_str}"
            if other_separate:
                sep_str = ", ".join([f"{get_coin_emoji(c)} {c}" for c in other_separate])
                coins_display += f"\n🔀 Also available: {sep_str}"
        mining_line = f"💎 Mining {coins_display}"
    else:
        # Single coin mode
        mining_line = f"💎 Mining **{coin}** ({algo_label})"
        # Check if this coin could be merge-mined with a parent
        if is_aux_chain(coin):
            parents = get_parent_chains(coin)
            mining_line += f"\n🔗 *Can be merge-mined with {', '.join(parents)}*"

    # Status bar
    status_bar = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    banner_title = theme("startup.banner")
    status_text = theme("startup.status")
    desc = f"""```ansi
\u001b[1;36m{status_bar}
{banner_title.center(len(status_bar))}
{status_bar}\u001b[0m
```

{mining_line} • {status_text}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    # Network & Odds section
    if net_phs and odds:
        lv, em = get_status_level(net_phs, coin)
        daily_pct = odds.get('daily_odds_pct', 0)
        weekly_pct = odds.get('weekly_odds_pct', 0)

        # Determine odds emoji based on daily percentage
        if daily_pct >= 50:
            odds_emoji = "🔥"
        elif daily_pct >= 25:
            odds_emoji = "✨"
        elif daily_pct >= 10:
            odds_emoji = "🎯"
        else:
            odds_emoji = "🎲"

        # Format network hashrate with algorithm-aware units
        net_hashrate_str = format_hashrate_phs(net_phs, symbol=coin)

        fields.append({
            "name": f"🌐 {coin_name} Network",
            "value": f"**Hashrate:** `{net_hashrate_str}`\n**Status:** {em} {lv}",
            "inline": False
        })

        fields.append({
            "name": f"{odds_emoji} Block Odds",
            "value": f"**Daily:** `{daily_pct:.1f}%`\n**Weekly:** `{weekly_pct:.1f}%`",
            "inline": False
        })

    # Fleet hashrate - use algorithm-aware formatting
    fleet_hashrate_str = format_hashrate_ths(fleet_ths, symbol=coin)
    fields.append({
        "name": "⛏️ Fleet Hashrate",
        "value": f"**Total:** `{fleet_hashrate_str}`",
        "inline": False
    })

    # Miner grid
    fields.append({
        "name": f"🖥️ Mining Fleet ({online}/{total} Online)",
        "value": "\n".join(miner_lines) if miner_lines else "No miners configured",
        "inline": False
    })

    # Available Coins section - show all pools offered by this server
    all_pools = fetch_all_pools()
    if len(all_pools) > 1:
        coin_lines = []
        for pool in all_pools:
            pool_coin = pool.get("coin", {})
            pool_symbol = pool_coin.get("symbol", pool_coin.get("type", "???")).upper()
            pool_name = pool_coin.get("name", pool_symbol)
            pool_algo = pool_coin.get("algorithm", "SHA256").upper()
            if pool_algo == "SHA256D":
                pool_algo = "SHA-256D"
            # Get stratum port from pool config
            ports = pool.get("ports", {})
            stratum_port = None
            for port_name, port_config in ports.items():
                if port_config.get("listenAddress"):
                    # Extract port from "0.0.0.0:3333" format
                    addr = port_config.get("listenAddress", "")
                    if ":" in addr:
                        stratum_port = addr.split(":")[-1]
                        break
            # Check if this is the currently active coin
            is_active = pool_symbol == coin.upper()
            status_icon = "✓ Active" if is_active else "Ready"
            port_str = f"Port {stratum_port}" if stratum_port else ""
            coin_emoji_line = get_coin_emoji(pool_symbol)
            line = f"{coin_emoji_line} **{pool_symbol}** ({pool_algo}) - {port_str} {status_icon}"
            coin_lines.append(line)

        if coin_lines:
            fields.append({
                "name": "🪙 Available Coins",
                "value": "\n".join(coin_lines),
                "inline": False
            })

    return _embed(
        f"🌀 SPIRAL SENTINEL v{__version__} {coin_emoji}",
        desc,
        COLORS["cyan"],
        fields,
        footer=theme("startup.footer", total=total, coin_name=coin_name)
    )

def create_report_embed(net_phs, fleet_ths, odds, md, diff=None, temps=None, prices=None, bri=None, trends=None, power=None, extremes=None, wallet_value=None, health_scores=None, blocks_found=None, daily_earnings=None, uptime_data=None, hashrate_trend=None, personal_bests=None, coin_symbol=None, diff_trends=None, infra_health=None, daily_summary=None, first_block_time=None, aux_diff_trends=None):
    """Enhanced 6-hour intel report with visual hierarchy and code blocks.

    Supports algorithm-aware hashrate formatting for both SHA-256d and Scrypt coins.
    Shows all enabled coins when in multi-coin/merge-mining mode.
    """
    # Determine active coin for volatility threshold - no default, use detected coin
    active_coin = coin_symbol.upper() if coin_symbol else None
    lv, em = get_status_level(net_phs, active_coin)
    coin_emoji = get_coin_emoji(active_coin)
    volatility_threshold = get_coin_volatility_threshold(active_coin)
    algorithm = get_coin_algorithm(active_coin)
    algo_label = "SCRYPT" if algorithm == "scrypt" else "SHA-256D"

    # Get all enabled coins to show in report (multi-coin/merge-mining support)
    enabled_coins = get_enabled_coins()
    all_coin_symbols = [c.get("symbol", "?").upper() for c in enabled_coins] if enabled_coins else [active_coin] if active_coin else []

    # Determine trend arrows for network and fleet
    net_trend_arrow = "➡️"
    if trends and "network" in trends:
        if trends["network"] > volatility_threshold: net_trend_arrow = "📈"
        elif trends["network"] < -volatility_threshold: net_trend_arrow = "📉"

    fleet_trend_arrow = "➡️"
    if hashrate_trend:
        direction, pct = hashrate_trend
        if direction == "up": fleet_trend_arrow = "📈"
        elif direction == "down": fleet_trend_arrow = "📉"

    color = COLORS["green"] if lv in ["AMAZING","GREAT"] else COLORS["blue"] if lv=="GOOD" else COLORS["yellow"] if lv=="NORMAL" else COLORS["red"]
    fields = []

    # Network section with code block formatting - algorithm-aware
    net_status = "+" if lv in ["AMAZING", "GREAT", "GOOD"] else "-" if lv == "BAD" else " "
    net_hashrate_str = format_hashrate_phs(net_phs, symbol=active_coin)
    nv = f"```diff\n{net_status} {net_hashrate_str}  {lv}\n```"
    nv += f"{em} {net_trend_arrow}"
    if diff:
        nv += f"\n🎯 Diff: `{format_difficulty(diff)}`"
    # Expected time to block (ETB) for this coin/hashrate combination
    if odds and odds.get("days_per_block", 0) > 0:
        etb_days = odds["days_per_block"]
        if etb_days >= 1:
            etb_str = f"{etb_days:.1f}d"
        elif etb_days * 24 >= 1:
            etb_str = f"{etb_days * 24:.1f}h"
        else:
            etb_str = f"{etb_days * 1440:.0f}m"
        nv += f"\n⏱️ ETB: `{etb_str}`"
    if extremes:
        if extremes.get("best"):
            best_time = extremes['best'].get('time', '')
            best_str = format_hashrate_phs(extremes['best']['phs'], symbol=active_coin)
            nv += f"\n🏆 Best: **{best_str}**" + (f" `{best_time}`" if best_time else "")
        if extremes.get("worst"):
            worst_time = extremes['worst'].get('time', '')
            worst_str = format_hashrate_phs(extremes['worst']['phs'], symbol=active_coin)
            nv += f"\n📈 Peak: **{worst_str}**" + (f" `{worst_time}`" if worst_time else "")
    fields.append({"name": "📡 NETWORK", "value": nv, "inline": False})

    # Difficulty & Hashrate Trends section (if trend data available)
    if diff_trends:
        trend_emoji = {"rising": "📈", "falling": "📉", "flat": "➡️"}
        algo_note = f" ({algo_label})"
        tv = f"**{active_coin} Difficulty{algo_note}**\n"

        # Display periods: 6h, 12h, 24h, 7d, 30d
        # Show "N/A" when insufficient data (< 2 samples) instead of misleading +0.0%
        for label, key in [("6h", "6h"), ("12h", "12h"), ("24h", "1d"), ("7d", "7d"), ("30d", "30d")]:
            d = diff_trends.get(key)
            if d and d.get("samples", 0) >= 2:
                d_arrow = trend_emoji.get(d.get("trend", "flat"), "➡️")
                tv += f"{label}: {d_arrow} {d.get('pct_change', 0):+.2f}%\n"
            else:
                tv += f"{label}: `N/A`\n"

        tv += f"*±{volatility_threshold}% threshold*"
        fields.append({"name": f"{coin_emoji} Trends", "value": tv, "inline": True})

    # Aux chain difficulty trends (merge mining)
    if aux_diff_trends:
        trend_emoji_aux = {"rising": "📈", "falling": "📉", "flat": "➡️"}
        for aux_coin, aux_trends in aux_diff_trends.items():
            aux_emoji = get_coin_emoji(aux_coin)
            aux_algo = get_coin_algorithm(aux_coin)
            aux_algo_label = "SCRYPT" if aux_algo == "scrypt" else "SHA-256D"
            aux_vt = get_coin_volatility_threshold(aux_coin)
            atv = f"**{aux_coin} Difficulty ({aux_algo_label})**\n"
            for label, key in [("6h", "6h"), ("12h", "12h"), ("24h", "1d"), ("7d", "7d"), ("30d", "30d")]:
                d = aux_trends.get(key)
                if d and d.get("samples", 0) >= 2:
                    d_arrow = trend_emoji_aux.get(d.get("trend", "flat"), "➡️")
                    atv += f"{label}: {d_arrow} {d.get('pct_change', 0):+.2f}%\n"
                else:
                    atv += f"{label}: `N/A`\n"
            atv += f"*±{aux_vt}% threshold*"
            fields.append({"name": f"{aux_emoji} {aux_coin} Trends", "value": atv, "inline": True})

    # Pool/Mining section with enhanced formatting - algorithm-aware
    fleet_hashrate_str = format_hashrate_ths(fleet_ths, symbol=active_coin)
    # Handle infinity ETB (when hashrate is 0) - display "N/A" instead of "inf"
    etb_display = f"~{odds['days_per_block']:.1f} days" if odds['days_per_block'] != float('inf') and odds['days_per_block'] < 99999 else "N/A"
    _daily_pct = odds['daily_odds_pct']
    pv = f"```yaml\nHashrate: {fleet_hashrate_str}\nShare:    {odds['share_pct']:.4f}%\nDaily:    {_daily_pct:.1f}%\nETB:      {etb_display}\n```"
    pv += f"{fleet_trend_arrow}"
    if blocks_found is not None and blocks_found > 0:
        pv += f" 🏆 **{blocks_found}** blocks!"
    # Highlight when odds exceed the HIGH ODDS threshold — makes it obvious in the 6h report
    if _daily_pct >= ODDS_TH:
        pool_label = "🎯 POOL — HIGH ODDS"
    else:
        pool_label = "⛏️ POOL"
    fields.append({"name": pool_label, "value": pv, "inline": True})

    # Block reward section (coin-aware) with code block
    if bri:
        reward_symbol = bri.get('symbol', active_coin)
        reward_raw = bri.get('sha256_reward', bri.get('scrypt_reward', bri.get('reward', bri.get('block_reward', 0))))
        reward_val = f"```fix\n{reward_raw:.1f} {reward_symbol}\n```"
        reward_val += f"📦 Block `#{bri['block_height']:,}`"
        fields.append({"name": f"{coin_emoji} REWARD", "value": reward_val, "inline": True})

    # Wallet section (coin-aware) with enhanced formatting
    if wallet_value is not None and prices:
        if active_coin in ("BTC", "BCH"):
            wv = f"```diff\n+ {wallet_value:.8f} {active_coin}\n```"
        else:
            wv = f"```diff\n+ {wallet_value:,.2f} {active_coin}\n```"
        wv += format_wallet_fiat(wallet_value, prices)
        fields.append({"name": "🏦 WALLET", "value": wv, "inline": True})

    # Rigs section with enhanced formatting
    rl = []
    total_power = 0
    online_count = 0
    offline_count = 0

    # Sort miners by IP address for consistent ordering
    def sort_key_ip(item):
        name = item[0]
        ip_match = re.search(r'(\d+)\.(\d+)\.(\d+)\.(\d+)', name)
        if ip_match:
            return tuple(int(x) for x in ip_match.groups())
        return (999, 999, 999, name)  # Non-IP names sort last

    sorted_md = sorted(md.items(), key=sort_key_ip)

    for n, hr in sorted_md:
        is_pool_only = hr == "Online (Stratum)"
        is_online = "OFFLINE" not in hr and not is_pool_only
        if is_pool_only:
            # ESP32/pool-only miners: not pollable, don't count as offline
            online_count += 1
            status_icon = "🎰"
        elif is_online:
            online_count += 1
            status_icon = "🟢"
        else:
            offline_count += 1
            status_icon = "🔴"

        # Build compact rig line (use display name: nickname > hostname > IP)
        display_name = get_miner_display_name(n)
        line = f"{status_icon} {display_name}: `{hr}`"

        # Add power consumption
        if power and n in power and power[n] > 0:
            line += f" ⚡`{power[n]:.0f}W`"
            total_power += power[n]

        # Add temperature with thermal indicator emoji
        # 🔥 = hot (>=80°C), 🌡️ = warm (>=70°C), ❄️ = cool (<70°C)
        if temps and n in temps:
            t = temps[n]
            chip_temp = t.get('chip') or t.get('board') or t.get('temp')
            if chip_temp:
                if chip_temp >= 80:
                    line += f" 🔥`{chip_temp:.0f}°`"
                elif chip_temp >= 70:
                    line += f" 🌡️`{chip_temp:.0f}°`"
                else:
                    line += f" ❄️`{chip_temp:.0f}°`"

        # Add uptime badge
        if uptime_data and n in uptime_data:
            uptime_pct = uptime_data[n]
            if uptime_pct >= 99.9:
                line += " 🏆"
            elif uptime_pct >= 99:
                line += " ⭐"
            elif uptime_pct < 90:
                line += " ⚠️"

        # Add connection quality score (0-100 health score)
        if health_scores and n in health_scores:
            hs = health_scores[n]
            hs_emoji = "💚" if hs >= 90 else "💛" if hs >= 75 else "🔴"
            line += f" {hs_emoji}`{hs:.0f}`"

        rl.append(line)

    # Fleet summary line
    fleet_summary = []
    if total_power > 0:
        fleet_summary.append(f"⚡ **{total_power:.0f}W**")
    if uptime_data:
        avg_uptime = sum(uptime_data.values()) / len(uptime_data) if uptime_data else 100
        fleet_summary.append(f"📊 **{avg_uptime:.1f}%** uptime")
    if fleet_summary:
        rl.append("─" * 20)
        rl.append(" • ".join(fleet_summary))

    # Rig status header
    rig_header = f"🟢 {online_count}" + (f" 🔴 {offline_count}" if offline_count > 0 else "")
    fields.append({"name": f"🖥️ RIGS ({rig_header})", "value": "\n".join(rl), "inline": False})

    # Earnings section with enhanced formatting
    if prices:
        ev = format_coin_price_yaml(active_coin, prices)
        if daily_earnings:
            coin_key = active_coin.lower() if active_coin else "dgb"
            daily_coin_amt = daily_earnings.get(coin_key, daily_earnings.get("dgb", 0))
            ev += f"📈 **{daily_coin_amt:.1f}** {active_coin}/day"
            ev += f"\n{format_currency_value(prices, dgb_amount=daily_coin_amt, show_dgb=False)}"
        elif odds and bri:
            # Use coin-specific reward instead of hardcoded 280
            coin_reward = bri.get('sha256_reward', bri.get('block_reward', 0))
            daily_coin = (odds['daily_odds_pct']/100) * coin_reward if coin_reward > 0 else 0
            ev += f"📈 **{daily_coin:.1f}** {active_coin}/day"
            ev += f"\n{format_currency_value(prices, dgb_amount=daily_coin, show_dgb=False)}"
        fields.append({"name": "💵 EARNINGS", "value": ev, "inline": True})

    # Electricity cost section
    power_data = fetch_power_cost()
    if power_data and not power_data.get("is_free_power"):
        sym = power_data.get("currency_symbol", "$")
        pv = f"```yaml\n"
        pv += f"Daily:   {sym}{power_data['daily_cost']:.2f} ({power_data['daily_kwh']:.1f} kWh)\n"
        pv += f"Monthly: {sym}{power_data['monthly_cost']:.2f}\n```"
        net = power_data.get("daily_profit", 0)
        margin = power_data.get("profit_margin_percent", 0)
        if net >= 0:
            pv += f"📈 Net: **+{sym}{net:.2f}**/day ({margin:.0f}% margin)"
        else:
            pv += f"📉 Net: **{sym}{net:.2f}**/day ({margin:.0f}% margin)"
        fields.append({"name": "⚡ POWER COST", "value": pv, "inline": True})
    elif power_data and power_data.get("is_free_power"):
        fields.append({"name": "⚡ POWER COST", "value": "```diff\n+ FREE POWER\n```", "inline": True})

    # Luck section with visual indicator
    if blocks_found is not None and blocks_found > 0 and odds.get('days_per_block', 0) > 0 and first_block_time:
        actual_elapsed_days = max((time.time() - first_block_time) / 86400, 0.01)  # Avoid div by zero
        expected_blocks = actual_elapsed_days / odds['days_per_block']
        luck_ratio = blocks_found / expected_blocks if expected_blocks > 0 else 1.0
        expected_days = blocks_found * odds['days_per_block']

        # Luck bar visualization
        if luck_ratio >= 1.5:
            luck_bar = "🍀🍀🍀🍀🍀"
            luck_status = "VERY LUCKY"
        elif luck_ratio >= 1.0:
            luck_bar = "🍀🍀🍀⚪⚪"
            luck_status = "LUCKY"
        elif luck_ratio >= 0.75:
            luck_bar = "🍀🍀⚪⚪⚪"
            luck_status = "AVERAGE"
        else:
            luck_bar = "🍀⚪⚪⚪⚪"
            luck_status = "UNLUCKY"

        luck_display = f"{luck_bar}\n**{luck_ratio:.2f}x** ({luck_status})"
        luck_display += f"\n`{blocks_found} blocks / {expected_days:.1f} exp days`"
        fields.append({"name": "🎲 LUCK", "value": luck_display, "inline": True})

    # Personal bests section
    if personal_bests:
        fields.append({"name": "🏅 RECORDS", "value": personal_bests, "inline": True})

    # Daily recap section (night report only)
    if daily_summary is not None:
        recap_lines = []
        blocks_today = daily_summary.get("blocks", 0)
        coin_rewards = daily_summary.get("coin_rewards", {})
        if blocks_today > 0:
            recap_lines.append(f"```diff\n+ {blocks_today} block{'s' if blocks_today != 1 else ''} found today!\n```")
            for coin, reward in sorted(coin_rewards.items()):
                coin_em = get_coin_emoji(coin)
                if coin in ("BTC", "BCH"):
                    recap_lines.append(f"{coin_em} **{reward:.8f}** {coin}")
                else:
                    recap_lines.append(f"{coin_em} **{reward:,.2f}** {coin}")
            miners = daily_summary.get("miners", [])
            if miners:
                recap_lines.append(f"Found by: {', '.join(miners[:5])}")
        else:
            recap_lines.append("```yaml\n0 blocks found today\n```")
            if odds and odds.get("daily_odds_pct", 0) > 0:
                recap_lines.append(f"Daily odds: **{odds['daily_odds_pct']:.1f}%** — tomorrow could be the day")
        fields.append({"name": "📋 TODAY'S RECAP", "value": "\n".join(recap_lines), "inline": False})

    # Infrastructure health section (if Prometheus metrics available)
    if infra_health and infra_health.metrics:
        infra_lines = []
        cb_state = infra_health.get_circuit_breaker_state()
        bp_level = infra_health.get_backpressure_level()
        zmq_health = infra_health.get_zmq_health()

        # Show circuit breaker status (only if not healthy)
        if cb_state > 0:
            infra_lines.append(f"⚡ Circuit: {infra_health.get_circuit_breaker_label()}")

        # Show backpressure status (only if elevated)
        if bp_level > 0:
            infra_lines.append(f"📊 Backpressure: {infra_health.get_backpressure_label()}")

        # Show ZMQ status (only if degraded)
        if zmq_health > 2:
            infra_lines.append(f"🔔 ZMQ: {infra_health.get_zmq_health_label()}")

        # Show blocks found/orphaned
        # Use blocks_found param (lifetime from DB) — Prometheus counter resets on pool restart
        orphans = infra_health.get_blocks_orphaned()
        lifetime_blocks = blocks_found if blocks_found is not None else 0
        if lifetime_blocks > 0 or orphans > 0:
            infra_lines.append(f"🏆 Blocks: `{lifetime_blocks}` found, `{orphans}` orphaned")

        # Show share loss if any
        shares_dropped = infra_health.get_shares_dropped()
        if shares_dropped > 0:
            infra_lines.append(f"⚠️ Shares lost: `{shares_dropped}`")

        # If all healthy, show brief healthy status
        if not infra_lines:
            infra_lines.append(f"🟢 Pipeline: Healthy | ZMQ: {infra_health.get_zmq_health_label()}")
            infra_lines.append(f"👥 Workers: `{infra_health.get_active_workers()}`")

        # Service uptime — always shown so operators notice unexpected restarts
        def _fmt_uptime(secs):
            secs = int(secs)
            d, r = divmod(secs, 86400)
            h, r = divmod(r, 3600)
            m = r // 60
            if d:   return f"{d}d {h}h {m}m"
            if h:   return f"{h}h {m}m"
            return f"{m}m"

        infra_lines.append(f"⏱️ Sentinel: `{_fmt_uptime(time.time() - SENTINEL_START_TIME)}`")

        try:
            import subprocess as _sp
            _r = _sp.run(
                ["systemctl", "show", "spiralstratum.service", "--property=ActiveEnterTimestamp"],
                capture_output=True, text=True, timeout=3
            )
            ts_line = _r.stdout.strip()  # "ActiveEnterTimestamp=Mon 2026-01-01 12:00:00 UTC"
            if _r.returncode == 0 and "=" in ts_line:
                ts_val = ts_line.split("=", 1)[1].strip()
                if ts_val and ts_val.lower() not in ("n/a", ""):
                    import datetime as _dt2
                    parts = ts_val.split()
                    if len(parts) >= 3:
                        ts_str = " ".join(parts[1:])   # "2026-01-01 12:00:00 UTC"
                        try:
                            if ts_str.endswith(" UTC"):
                                dt = _dt2.datetime.strptime(ts_str, "%Y-%m-%d %H:%M:%S UTC")
                                dt = dt.replace(tzinfo=_dt2.timezone.utc)
                                stratum_secs = (datetime.now(timezone.utc) - dt).total_seconds()
                            else:
                                dt = _dt2.datetime.strptime(ts_str.rsplit(" ", 1)[0], "%Y-%m-%d %H:%M:%S")
                                stratum_secs = time.time() - dt.timestamp()
                            if stratum_secs > 0:
                                infra_lines.append(f"⏱️ Stratum: `{_fmt_uptime(stratum_secs)}`")
                        except ValueError:
                            pass
        except Exception:
            pass  # systemd not available or service not found — skip silently

        if infra_lines:
            fields.append({"name": "🔧 INFRASTRUCTURE", "value": "\n".join(infra_lines), "inline": True})

    # ── Backup status (only when cron is installed — user opted in during setup) ──
    if os.path.exists("/etc/cron.d/spiralpool-backup"):
        try:
            newest_mtime = _get_newest_backup_time()
            import datetime as _dt
            bv_lines = []
            if newest_mtime:
                age_secs = time.time() - newest_mtime
                last_str = _dt.datetime.fromtimestamp(newest_mtime).strftime("%Y-%m-%d %H:%M")
                if age_secs < 86400:
                    age_str = f"{int(age_secs // 3600)}h ago"
                else:
                    age_str = f"{age_secs / 86400:.1f}d ago"
                # Get backup count
                backup_count = 0
                try:
                    backup_count = len([d for d in os.listdir("/spiralpool/backups")
                                        if d.startswith("daily-") and os.path.isdir(f"/spiralpool/backups/{d}")])
                except Exception:
                    pass
                status_em = "🟢" if age_secs < 86400 * CONFIG.get("backup_stale_days", 2) else "🔴"
                bv_lines.append(f"{status_em} Last: `{last_str}` ({age_str})")
                bv_lines.append(f"💾 Snapshots: `{backup_count}`")
            else:
                bv_lines.append("🔴 No backups found")
            # Next scheduled run from cron expression
            try:
                with open("/etc/cron.d/spiralpool-backup") as _cf:
                    for _line in _cf:
                        _line = _line.strip()
                        if _line and not _line.startswith("#"):
                            _parts = _line.split()
                            if len(_parts) >= 5:
                                bv_lines.append(f"⏰ Schedule: `{' '.join(_parts[:5])}`")
                            break
            except Exception:
                pass
            if bv_lines:
                fields.append({"name": "💿 BACKUPS", "value": "\n".join(bv_lines), "inline": True})
        except Exception as _e:
            logger.debug(f"Backup status in report error: {_e}")

    # Dynamic title and description based on report frequency and time of day
    if REPORT_FREQUENCY == "daily":
        title = theme("report.title_daily", coin_emoji=coin_emoji)
    elif _is_final_report_now():
        title = theme("report.title_goodnight")
    elif local_now().hour < 9:
        title = theme("report.title_morning")
    else:
        title = theme("report.title_default", coin_emoji=coin_emoji)

    # Status-based description
    status_desc = f"```ansi\n\u001b[1;36m{'═' * 29}\n   SENTINEL STATUS: {lv.upper()}\n{'═' * 29}\u001b[0m\n```" if lv in ["AMAZING", "GREAT"] else ""

    # Show all enabled coins with proper merge mining relationships
    is_merging, merge_summary, aux_coins = get_merge_mining_summary(active_coin)

    if is_merging and aux_coins:
        # Actual merge mining: show parent + aux chains
        coins_display = f"**{active_coin}** {merge_summary}"
        desc = f"{status_desc}📅 `{local_now().strftime('%Y-%m-%d %H:%M')}` • Mining {coins_display}"
    elif len(all_coin_symbols) > 1:
        # Multi-coin pool mode: distinguish merge-mineable from separate pools
        other_coins = [c for c in all_coin_symbols if c != active_coin]
        coins_display = f"**{active_coin}**"
        if other_coins:
            # Categorize: actual AuxPoW merge vs separate pools
            merge_coins = [c for c in other_coins if is_aux_chain(c) and active_coin in get_parent_chains(c)]
            separate_coins = [c for c in other_coins if c not in merge_coins]
            if merge_coins:
                merge_str = ", ".join([f"{get_coin_emoji(c)} {c}" for c in merge_coins])
                coins_display += f" 🔗 +{merge_str}"
            if separate_coins:
                sep_str = ", ".join([f"{get_coin_emoji(c)} {c}" for c in separate_coins])
                coins_display += f" 🔀 +{sep_str}"
        desc = f"{status_desc}📅 `{local_now().strftime('%Y-%m-%d %H:%M')}` • Mining {coins_display}"
    else:
        # Solo mode: just show the primary coin
        desc = f"{status_desc}📅 `{local_now().strftime('%Y-%m-%d %H:%M')}` • Mining **{active_coin}**"
        if is_aux_chain(active_coin):
            parents = get_parent_chains(active_coin)
            desc += f" *(merge-mineable with {', '.join(parents)})*"

    return _embed(title, desc, color, fields, footer=theme("report.footer", version=__version__, next_report=_next_report_label()))

def create_block_embed(block_num, prices=None, bri=None, found_by=None, miner_details=None, pool_block_num=None, coin_symbol=None, time_since_last=None, effort_pct=None, block_hash=None, network_hashrate=None, difficulty=None, observed_hashrate_hs=None):
    """Cyberpunk block capture celebration with miner details - MAXIMUM HYPE EDITION!

    Supports multi-coin with appropriate emojis and formatting:
    - DGB: 💎 DigiByte
    - BTC: 🟠 Bitcoin
    - BCH: 🟢 Bitcoin Cash

    🎉 BLOCK CAPTURED! 🎉
    🔥 You flatlined the network, choom! 🔥
    """
    # Determine coin - use detected coin, no default
    coin = coin_symbol.upper() if coin_symbol else None
    coin_emoji = get_coin_emoji(coin) if coin else "🪙"
    coin_name = get_coin_name(coin) if coin else "Unknown"

    # Get block reward - use coin-specific reward if available (not hardcoded)
    if bri:
        br = bri.get("sha256_reward", bri.get("scrypt_reward", bri.get("reward", bri.get("block_reward", DEFAULT_BLOCK_REWARDS.get(coin, 0)))))
    else:
        br = DEFAULT_BLOCK_REWARDS.get(coin, 0) if coin else 0

    fields = []

    # Celebration header with ASCII art style
    celebration_bar = "★ ═══════════════════════════════ ★"
    block_banner = theme("block.banner")
    block_hero = theme("block.hero")
    block_sub = theme("block.sub")

    # Epic description with celebration
    desc = f"""```ansi
\u001b[1;33m{celebration_bar}
{block_banner.center(len(celebration_bar))}
{celebration_bar}\u001b[0m
```

{block_hero}

{block_sub}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    # Build reward and value info
    coin_display = coin or "???"
    if coin in ["BTC", "BCH"]:
        reward_str = f"`{br:.8f} {coin_display}`"
    else:
        reward_str = f"`{br:.2f} {coin_display}`"

    # Value calculation — respects user's REPORT_CURRENCY preference
    if coin and prices:
        cur = get_currency_meta()
        fiat_val = br * (prices.get(cur["code"], 0) or 0)
        value_str = f"`{cur['symbol']}{fiat_val:,.{cur['decimals']}f} {REPORT_CURRENCY}`" if fiat_val > 0 else "Price N/A"
    else:
        value_str = "Price N/A"

    # Block details
    block_info_lines = []
    if block_num:
        block_info_lines.append(f"**Chain Height:** `{block_num:,}`")
    if pool_block_num:
        block_info_lines.append(f"**Pool Block:** `#{pool_block_num}`")
    block_info_lines.append(f"**Reward:** {reward_str}")
    block_info_lines.append(f"**Value:** {value_str}")

    # Time since last block and effort
    if time_since_last is not None and time_since_last > 0:
        if time_since_last >= 86400:
            time_str = f"{time_since_last / 86400:.1f} days"
        elif time_since_last >= 3600:
            time_str = f"{time_since_last / 3600:.1f} hours"
        else:
            time_str = f"{time_since_last / 60:.0f} min"
        block_info_lines.append(f"**Time Since Last:** `{time_str}`")
    if effort_pct is not None:
        if effort_pct < 100:
            luck_label = "Lucky! 🍀"
        elif effort_pct < 200:
            luck_label = "Normal"
        else:
            luck_label = "Overdue"
        block_info_lines.append(f"**Effort:** `{effort_pct:.1f}%` ({luck_label})")

    # Network stats at time of block find
    if network_hashrate and network_hashrate > 0:
        block_info_lines.append(f"**Network Hashrate:** `{format_hashrate(network_hashrate * 1e15, coin)}`")
    if difficulty and difficulty > 0:
        block_info_lines.append(f"**Difficulty:** `{format_difficulty(difficulty)}`")
    if observed_hashrate_hs and observed_hashrate_hs > 0:
        block_info_lines.append(f"**Observed Hashrate:** `{format_hashrate(observed_hashrate_hs, coin)}`")

    fields.append({
        "name": f"{coin_emoji} Block Reward",
        "value": "\n".join(block_info_lines),
        "inline": False
    })

    # Miner details - who's the hero?
    found_by_text = theme("block.found_by")
    if miner_details:
        hero_names = []
        for name, info in miner_details.items():
            if info.get("found_block"):
                hero_names.append(name)

        if hero_names:
            hero_display = "\n".join([f"⚡ **{h}** {found_by_text}" for h in hero_names])
            fields.append({
                "name": "🎯 The Hero",
                "value": hero_display,
                "inline": False
            })
        elif found_by:
            # Worker name from pool API didn't match any miner name — show it anyway
            fields.append({
                "name": "🎯 Captured By",
                "value": f"⚡ **{found_by}** {found_by_text}",
                "inline": False
            })
    elif found_by:
        fields.append({
            "name": "🎯 Captured By",
            "value": f"⚡ **{found_by}** {found_by_text}",
            "inline": False
        })

    block_footer_text = theme("block.footer")
    footer = f"🌀 {coin_name} Block #{pool_block_num or '?'} • {local_now().strftime('%Y-%m-%d %H:%M:%S')} • {block_footer_text}"

    # Explorer link — makes the embed title clickable in Discord; also shown as a field for Telegram/ntfy
    explorer_url = get_block_explorer_url(coin, block_hash)
    if explorer_url:
        fields.append({
            "name": "🔍 View Block",
            "value": f"[Open on Explorer]({explorer_url})",
            "inline": False
        })

    embed = _embed(theme("block.title", coin=coin_display), desc, COLORS["purple"], fields, footer=footer)
    if explorer_url:
        embed["url"] = explorer_url  # Makes Discord embed title a hyperlink
    return embed


def create_block_orphaned_embed(block_height, pool_block_num=None, coin_symbol=None, found_at=None, orphaned_at=None, block_reward=None, prices=None):
    """
    CRITICAL ALERT: Block was orphaned after being found.
    This is a P0 audit fix - orphaned blocks MUST be visible immediately.

    Args:
        block_height: The blockchain height of the orphaned block
        pool_block_num: Pool's internal block number (if available)
        coin_symbol: The coin symbol (DGB, BTC, etc.)
        found_at: When the block was originally found (datetime or string)
        orphaned_at: When the block was detected as orphaned (datetime or string)
        block_reward: The block reward amount (float, in coin units)
        prices: Price dict for fiat value calculation
    """
    coin = coin_symbol.upper() if coin_symbol else "UNKNOWN"
    coin_emoji = get_coin_emoji(coin) if coin_symbol else "🪙"
    coin_name = get_coin_name(coin) if coin_symbol else "Unknown"

    # Build description with urgency
    orphan_emoji = theme("orphaned.banner_emoji")
    orphan_flavor = theme("orphaned.flavor")
    orphan_bar = "⚠═══════════════════════════════⚠"
    desc = f"""```ansi
\u001b[1;31m{orphan_bar}
{"⚠️ BLOCK ORPHANED ⚠️".center(len(orphan_bar))}
{orphan_bar}\u001b[0m
```

{orphan_emoji} **{orphan_flavor}**

This means another miner's block was accepted by the network instead.
The block reward has been **LOST**.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = []

    # Block details
    block_info = []
    if pool_block_num:
        block_info.append(f"**Pool Block:** `#{pool_block_num}`")
    block_info.append(f"**Height:** `{block_height}`")
    block_info.append(f"**Coin:** `{coin}`")

    if found_at:
        found_str = found_at if isinstance(found_at, str) else found_at.strftime('%Y-%m-%d %H:%M:%S')
        block_info.append(f"**Found At:** `{found_str}`")

    if orphaned_at:
        orphaned_str = orphaned_at if isinstance(orphaned_at, str) else orphaned_at.strftime('%Y-%m-%d %H:%M:%S')
        block_info.append(f"**Orphaned At:** `{orphaned_str}`")

    # Revenue lost estimate
    if block_reward and block_reward > 0:
        if coin in ("BTC", "BCH"):
            block_info.append(f"**Revenue Lost:** `{block_reward:.8f} {coin}`")
        else:
            block_info.append(f"**Revenue Lost:** `{block_reward:,.2f} {coin}`")
        if prices:
            cur = get_currency_meta()
            fiat_lost = block_reward * (prices.get(cur["code"], 0) or 0)
            if fiat_lost > 0:
                block_info.append(f"**Value Lost:** `{cur['symbol']}{fiat_lost:,.{cur['decimals']}f} {REPORT_CURRENCY}`")

    fields.append({
        "name": f"{coin_emoji} Orphaned Block Details",
        "value": "\n".join(block_info),
        "inline": False
    })

    # Investigation tips
    fields.append({
        "name": "🔍 Investigation",
        "value": """• Check block explorer for competing blocks at this height
• Review network latency and block propagation
• Verify ZMQ block notifications are working
• Consider if pool was on a minority chain fork""",
        "inline": False
    })

    footer = f"🌀 {coin_name} • Orphan detected at {local_now().strftime('%Y-%m-%d %H:%M:%S')} • Check blockchain explorer"

    return _embed(theme("orphaned.title", coin=coin), desc, COLORS["red"], fields, footer=footer)


def create_miner_offline_embed(n, m, miner_ip=None):
    """Sentinel alert: Miner went dark - Enhanced with visual urgency and quick actions"""
    display_name = get_miner_display_name(n)

    # Urgency level based on time offline
    if m >= 30:
        urgency = "🔴🔴🔴 CRITICAL"
        urgency_note = "Extended downtime - check hardware!"
    elif m >= 10:
        urgency = "🔴🔴 WARNING"
        urgency_note = "May need attention soon"
    else:
        urgency = "🔴 ALERT"
        urgency_note = "Monitoring for recovery..."

    # Format duration nicely
    if m >= 60:
        duration_str = f"{m // 60}h {m % 60}m"
    else:
        duration_str = f"{m} minutes"

    offline_banner = theme("offline.banner")
    desc = f"""```diff
- {offline_banner}
```

{urgency}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🖥️ Miner Details",
            "value": f"**Name:** `{display_name}`\n**Offline:** `{duration_str}`\n**Status:** {urgency_note}",
            "inline": False
        },
    ]

    # Quick action footer with SSH hint if IP available
    if miner_ip:
        fields.append({
            "name": "🔧 Quick Actions",
            "value": f"**SSH:** `ssh root@{miner_ip}`\n**Ping:** `ping {miner_ip}`",
            "inline": False
        })
        footer = theme("offline.footer")
    else:
        footer = theme("offline.footer_noip")

    return _embed(
        theme("offline.title"),
        desc,
        COLORS["red"],
        fields,
        footer=footer
    )


def create_miner_online_embed(n, m, miner_ip=None, hashrate_ghs=None, temp_c=None):
    """Sentinel alert: Miner back online - Celebration mode with recovery context"""
    display_name = get_miner_display_name(n)

    # Format downtime nicely
    if m >= 60:
        downtime_str = f"{m // 60}h {m % 60}m"
    else:
        downtime_str = f"{m} min"

    online_banner = theme("online.banner")
    online_flavor = theme("online.flavor")
    desc = f"""```diff
+ {online_banner}
```

✨ **{display_name}** {online_flavor}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    recovery_details = f"**Miner:** `{display_name}`\n**Was Offline:** `{downtime_str}`"
    if hashrate_ghs is not None and hashrate_ghs > 0:
        recovery_details += f"\n**Hashrate:** `{hashrate_ghs:.0f} GH/s`"
    if temp_c is not None and temp_c > 0:
        recovery_details += f"\n**Temp:** `{temp_c}°C`"

    fields = [
        {
            "name": "🖥️ Recovery Details",
            "value": recovery_details,
            "inline": False
        },
    ]

    # Quick action with SSH hint if IP available
    if miner_ip:
        fields.append({
            "name": "🔧 Quick Actions",
            "value": f"**SSH:** `ssh root@{miner_ip}`\n**Ping:** `ping {miner_ip}`",
            "inline": False
        })

    return _embed(
        theme("online.title"),
        desc,
        COLORS["green"],
        fields,
        footer=theme("online.footer")
    )


def create_restart_embed(n, m, ok):
    """Sentinel alert: Auto-restart triggered - Enhanced feedback"""
    display_name = get_miner_display_name(n)

    if ok:
        status_icon = "✅"
        status_text = "Signal Sent"
        success_banner = theme("restart.success_banner")
        desc = f"""```fix
{success_banner}
```

🔄 Restart signal sent to **{display_name}**

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""
        color = COLORS["orange"]
    else:
        status_icon = "❌"
        status_text = "FAILED"
        fail_banner = theme("restart.fail_banner")
        desc = f"""```diff
- {fail_banner}
```

⚠️ Manual intervention may be required for **{display_name}**

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""
        color = COLORS["red"]

    fields = [
        {
            "name": "🔄 Restart Details",
            "value": f"**Target:** `{display_name}`\n**Offline:** `{m} min`\n**Result:** {status_icon} {status_text}",
            "inline": False
        },
    ]

    return _embed(
        theme("restart.title"),
        desc,
        color,
        fields,
        footer=theme("restart.footer")
    )


def create_temp_embed(n, t, lvl, miner_ip=None):
    """Sentinel alert: Thermal warning/critical - Enhanced thermal alerts"""
    display_name = get_miner_display_name(n)

    # Temperature visualization
    if t >= 90:
        temp_bar = "🔴🔴🔴🔴🔴"
        temp_status = "DANGER"
    elif t >= 80:
        temp_bar = "🟠🟠🟠🟠⚪"
        temp_status = "HIGH"
    elif t >= 70:
        temp_bar = "🟡🟡🟡⚪⚪"
        temp_status = "WARM"
    else:
        temp_bar = "🟢🟢⚪⚪⚪"
        temp_status = "OK"

    if lvl == "CRITICAL":
        title = theme("temp.critical_title")
        desc = f"""```diff
- {theme("temp.critical_banner")}
```

{temp_bar} **{t}°C** - {temp_status}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""
        color = COLORS["red"]
        footer = theme("temp.critical_footer")
    else:
        title = theme("temp.warning_title")
        desc = f"""```fix
{theme("temp.warning_banner")}
```

{temp_bar} **{t}°C** - {temp_status}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""
        color = COLORS["yellow"]
        footer = theme("temp.warning_footer")

    details = f"**Miner:** `{display_name}`\n**Temperature:** `{t}°C`\n**Status:** {temp_status} {temp_bar}"

    fields = [
        {
            "name": "🌡️ Thermal Details",
            "value": details,
            "inline": False
        },
    ]

    if miner_ip:
        fields.append({
            "name": "🔧 Quick Actions",
            "value": f"**SSH:** `ssh root@{miner_ip}`\n**Ping:** `ping {miner_ip}`",
            "inline": False
        })

    return _embed(title, desc, color, fields, footer=footer)


def create_miner_reboot_embed(name, old_uptime_sec, new_uptime_sec, miner_ip=None):
    """Sentinel alert: Miner reboot detected with detailed uptime info - ENHANCED

    Consolidated reboot embed - handles both blip detection and explicit reboot alerts.
    """
    display_name = get_miner_display_name(name)
    old_mins = old_uptime_sec // 60
    old_secs = old_uptime_sec % 60
    new_mins = new_uptime_sec // 60
    new_secs = new_uptime_sec % 60

    reboot_flavor = theme("reboot.flavor")
    desc = f"""```fix
REBOOT DETECTED
```

🔄 **{display_name}** {reboot_flavor}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "⏱️ Uptime Change",
            "value": f"**Miner:** `{display_name}`\n**Before:** `{old_mins}m {old_secs}s`\n**After:** `{new_mins}m {new_secs}s`",
            "inline": False
        },
        {
            "name": "🔍 Possible Causes",
            "value": "• Power fluctuation\n• Network hiccup\n• Pool connection drop",
            "inline": False
        },
    ]

    # Quick action footer with SSH hint if IP available
    footer = theme("reboot.footer")
    if miner_ip:
        footer += f" • SSH: ssh root@{miner_ip}"

    return _embed(theme("reboot.title"), desc, COLORS["yellow"], fields, footer=footer)


def create_blip_embed(n, d=None):
    """Sentinel alert: Power blip / reboot detected - LEGACY WRAPPER

    Calls the consolidated create_miner_reboot_embed for backwards compatibility.
    """
    old_sec = d.get('old', 0) if d else 0
    new_sec = d.get('new', 0) if d else 0
    return create_miner_reboot_embed(n, old_sec, new_sec)


def create_zombie_embed(n, i):
    """Sentinel alert: Zombie miner detected - ENHANCED"""
    display_name = get_miner_display_name(n)

    zombie_banner = theme("zombie.banner")
    zombie_flavor = theme("zombie.flavor")
    zombie_bar = "⚠═══════════════════════════════⚠"
    desc = f"""```ansi
\u001b[1;31m{zombie_bar}
{zombie_banner.center(len(zombie_bar))}
{zombie_bar}\u001b[0m
```

**{display_name}** is online but not submitting valid work!

⚠️ This miner {zombie_flavor}.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    zombie_details = f"**Miner:** `{display_name}`\n**Reason:** `{i['reason']}`"
    if i.get('hashrate') is not None:
        zombie_details += f"\n**Reported Hashrate:** `{i['hashrate']:.0f} GH/s`"
    if i.get('duration_mins') is not None:
        zombie_details += f"\n**Zombie Duration:** `{i['duration_mins']} min`"

    fields = [
        {
            "name": "🧟 Zombie Details",
            "value": zombie_details,
            "inline": False
        },
    ]

    return _embed(theme("zombie.title"), desc, COLORS["red"], fields, footer=theme("zombie.footer"))


def create_degradation_embed(i, temp_c=None):
    """Sentinel alert: Hashrate degradation - ENHANCED"""
    drop_pct = i['drop_pct']
    display_name = get_miner_display_name(i['name'])

    # Severity visualization
    if drop_pct >= 30:
        drop_bar = "📉📉📉📉📉"
        severity = "SEVERE"
    elif drop_pct >= 20:
        drop_bar = "📉📉📉📉⚪"
        severity = "SIGNIFICANT"
    elif drop_pct >= 10:
        drop_bar = "📉📉📉⚪⚪"
        severity = "MODERATE"
    else:
        drop_bar = "📉📉⚪⚪⚪"
        severity = "MINOR"

    desc = f"""```fix
PERFORMANCE DEGRADATION
```

**{display_name}** is running below baseline

⚠️ **{severity}** decline detected

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    hr_details = f"**Miner:** `{display_name}`\n**Baseline:** `{i['baseline']:.0f} GH/s`\n**Current:** `{i['current']:.0f} GH/s`\n**Drop:** `{drop_pct:.0f}%` {drop_bar}"
    if temp_c is not None and temp_c > 0:
        hr_details += f"\n**Current Temp:** `{temp_c}°C`"

    fields = [
        {
            "name": "📉 Hashrate Change",
            "value": hr_details,
            "inline": False
        },
        {
            "name": "🔧 Check These",
            "value": "• Chip temps and cooling\n• Power supply stability\n• Network connectivity",
            "inline": False
        },
    ]

    return _embed(theme("degradation.title"), desc, COLORS["orange"], fields, footer=theme("degradation.footer"))


def create_chronic_issue_embed(name, issue_info):
    """Sentinel alert: Chronic/recurring issue detected - miner needs attention."""
    display_name = get_miner_display_name(name)
    alert_type = issue_info.get("type", "unknown")
    count = issue_info.get("count", 0)
    duration_hours = issue_info.get("duration_hours", 0)

    # Map alert types to human-readable descriptions
    issue_map = {
        "miner_offline": ("🔴 Going offline repeatedly", "connectivity or power"),
        "temp_warning": ("🌡️ Temperature warnings", "cooling system"),
        "temp_critical": ("🔥 Critical temperatures", "cooling urgently"),
        "miner_reboot": ("🔄 Frequent reboots", "power or stability"),
        "zombie_miner": ("🧟 Zombie state", "share submission"),
        "degradation": ("📉 Hashrate degradation", "performance"),
    }

    issue_desc, check_area = issue_map.get(alert_type, (f"⚠️ {alert_type}", "hardware"))

    chronic_bar = "⚠═══════════════════════════════⚠"
    banner_text = theme("chronic.banner")
    desc = f"""```ansi
\u001b[1;33m{chronic_bar}
{banner_text:^{len(chronic_bar)}}
{chronic_bar}\u001b[0m
```

**{display_name}** {theme("chronic.body")}

⚠️ Same issue has occurred **{count}x** over **{duration_hours:.1f} hours**

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🔁 Issue Pattern",
            "value": f"**Miner:** `{display_name}`\n**Issue:** {issue_desc}\n**Occurrences:** `{count}` times\n**Duration:** `{duration_hours:.1f}` hours",
            "inline": False
        },
        {
            "name": "🔧 Recommended Action",
            "value": f"• Check {check_area}\n• Review miner logs\n• Consider hardware inspection",
            "inline": False
        },
    ]

    return _embed(theme("chronic.title"), desc, COLORS["orange"], fields, footer=f"🌀 Spiral Sentinel v{__version__} • {theme('chronic.footer')}")


def create_excessive_restarts_embed(name, count, window_hours):
    """Sentinel alert: Miner restarting too frequently."""
    display_name = get_miner_display_name(name)

    desc = f"""```fix
{theme("excessive_restarts.banner")}
```

**{display_name}** has restarted **{count}x** in the last **{window_hours} hour(s)**!

⚠️ {theme("excessive_restarts.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🔄 Restart Pattern",
            "value": f"**Miner:** `{display_name}`\n**Restarts:** `{count}` in `{window_hours}h`\n**Rate:** `{count/window_hours:.1f}` per hour",
            "inline": False
        },
        {
            "name": "🔧 Likely Causes",
            "value": "• Unstable power supply\n• Overheating (thermal shutdown)\n• Network/pool connection issues\n• Hardware fault",
            "inline": False
        },
    ]

    return _embed(theme("excessive_restarts.title"), desc, COLORS["red"], fields, footer=theme("excessive_restarts.footer"))


def create_hashrate_divergence_embed(name, miner_hr_ghs, pool_hr_ghs, divergence_count):
    """Sentinel alert: Pool hashrate much lower than miner claims."""
    display_name = get_miner_display_name(name)

    # Calculate the percentage pool is receiving
    if miner_hr_ghs > 0:
        receive_pct = (pool_hr_ghs / miner_hr_ghs) * 100
    else:
        receive_pct = 0

    # Format hashrates for display
    if miner_hr_ghs >= 1000:
        miner_hr_str = f"{miner_hr_ghs/1000:.2f} TH/s"
    else:
        miner_hr_str = f"{miner_hr_ghs:.0f} GH/s"

    if pool_hr_ghs >= 1000:
        pool_hr_str = f"{pool_hr_ghs/1000:.2f} TH/s"
    else:
        pool_hr_str = f"{pool_hr_ghs:.0f} GH/s"

    desc = f"""```diff
- {theme("divergence.banner")}
```

**{display_name}** claims `{miner_hr_str}` but pool only sees `{pool_hr_str}`!

⚠️ Pool receiving only **{receive_pct:.0f}%** of claimed hashrate.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "📊 Hashrate Comparison",
            "value": f"**Miner Reports:** `{miner_hr_str}`\n**Pool Sees:** `{pool_hr_str}`\n**Difference:** `{receive_pct:.0f}%` received\n**Divergence:** `{divergence_count}` consecutive checks",
            "inline": False
        },
        {
            "name": "🔍 Possible Causes",
            "value": "• Network/pool connectivity issues\n• High share rejection rate\n• Miner misconfiguration\n• Stale work submission",
            "inline": False
        },
    ]

    return _embed(theme("divergence.title"), desc, COLORS["orange"], fields, footer=theme("divergence.footer"))


# ═══════════════════════════════════════════════════════════════════════════════
# NEW ALERT EMBEDS - Thermal Protection + B1-B8 Monitoring Alerts
# ═══════════════════════════════════════════════════════════════════════════════

def create_thermal_shutdown_embed(name, temp, stopped, miner_type):
    """Sentinel alert: Thermal emergency - miner frequency set to 0 (or could not be stopped)."""
    display_name = get_miner_display_name(name)
    miner_ip = _miner_ip_lookup.get(name, "unknown")

    if stopped:
        status_text = theme("thermal.stopped")
        status_icon = "🛑"
        action_text = "Frequency has been set to **0** - the ASIC is no longer hashing."
        instruction = "You **MUST** manually restore your frequency via the AxeOS web interface or API."
    else:
        status_text = theme("thermal.failed")
        status_icon = "🚨"
        action_text = f"Sentinel was **unable** to stop this miner (type: `{miner_type}`)."
        instruction = "**UNPLUG THE MINER IMMEDIATELY** to prevent hardware damage!"

    thermal_bar = "🔥═══════════════════════════════🔥"
    banner_text = theme("thermal.banner")
    desc = f"""```ansi
\u001b[1;31m{thermal_bar}
{f"{status_icon} {banner_text} {status_icon}":^{len(thermal_bar)}}
{thermal_bar}\u001b[0m
```

**{status_text}**

{action_text}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🌡️ Temperature",
            "value": f"**Miner:** `{display_name}`\n**IP:** `{miner_ip}`\n**Chip Temp:** `{temp}°C`\n**Type:** `{miner_type}`",
            "inline": False
        },
        {
            "name": "⚠️ Required Action",
            "value": instruction,
            "inline": False
        },
    ]

    return _embed(f"{status_icon} {theme('thermal.title')}", desc, COLORS["red"], fields, footer=theme("thermal.footer"))


def create_fan_failure_embed(name, fan_speeds, chip_temp):
    """Sentinel alert: Fan failure detected - one or more fans at 0 RPM."""
    display_name = get_miner_display_name(name)

    fan_list = "\n".join(f"  Fan {i+1}: `{rpm} RPM` {'🔴' if rpm == 0 else '🟢'}" for i, rpm in enumerate(fan_speeds))

    desc = f"""```fix
{theme("fan.banner")}
```

**{display_name}** has one or more fans at **0 RPM**

⚠️ {theme("fan.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🌀 Fan Status",
            "value": f"**Miner:** `{display_name}`\n{fan_list}\n**Chip Temp:** `{chip_temp}°C`",
            "inline": False
        },
        {
            "name": "🔧 Check These",
            "value": "• Fan connectors and cables\n• Fan bearings (listen for noise)\n• Dust buildup on heatsinks\n• Replace failed fan ASAP",
            "inline": False
        },
    ]

    return _embed(theme("fan.title"), desc, COLORS["red"], fields, footer=theme("fan.footer"))


def create_rejection_spike_embed(name, reject_pct, accepted, rejected, stale=0, stale_pct=0):
    """Sentinel alert: Share rejection rate exceeds 20% (excluding stale shares)."""
    display_name = get_miner_display_name(name)

    if reject_pct >= 35:
        severity = "SEVERE"
        bar = "🔴🔴🔴🔴🔴"
    elif reject_pct >= 25:
        severity = "HIGH"
        bar = "🔴🔴🔴🟡🟡"
    else:
        severity = "ELEVATED"
        bar = "🔴🔴🟡🟡🟡"

    desc = f"""```fix
{theme("rejection.banner")}
```

**{display_name}** rejection rate is **{reject_pct:.1f}%** {bar}

⚠️ **{severity}** - {theme("rejection.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    stale_line = f"\n**Stale:** `{stale}` (`{stale_pct:.1f}%` — excluded from reject rate)" if stale > 0 else ""
    fields = [
        {
            "name": "📊 Share Stats",
            "value": f"**Miner:** `{display_name}`\n**Rejection Rate:** `{reject_pct:.1f}%`\n**Accepted:** `{accepted}`\n**Rejected:** `{rejected}`{stale_line}",
            "inline": False
        },
        {
            "name": "🔧 Likely Causes",
            "value": "• Overclocking too aggressive\n• Network latency/packet loss\n• Hardware errors (check ASIC chips)\n• Stratum connection issues",
            "inline": False
        },
    ]

    return _embed(theme("rejection.title"), desc, COLORS["orange"], fields, footer=theme("rejection.footer"))


def create_orphan_spike_embed(new_orphans, total_orphans, coin=None, block_reward=None, prices=None):
    """Sentinel alert: New orphaned block(s) detected from Prometheus metrics.

    Args:
        new_orphans: Number of new orphans detected
        total_orphans: Running total of orphaned blocks
        coin: Optional coin symbol for revenue calculation
        block_reward: Optional block reward amount for revenue calculation
        prices: Optional price dict with 'usd'/'cad' keys for revenue calculation
    """

    # Calculate revenue loss if data is available
    revenue_line = ""
    if new_orphans and block_reward and prices:
        lost_coins = new_orphans * block_reward
        coin_str = coin or "coins"
        if coin_str in ("BTC", "BCH"):
            coin_amount = f"{lost_coins:.8f} {coin_str}"
        else:
            coin_amount = f"{lost_coins:,.2f} {coin_str}"

        revenue_line = f"\n\n💸 **Estimated Revenue Lost:** `{coin_amount}`"
        revenue_line += format_fiat_inline(lost_coins, prices)

    orphan_spike_emoji = theme("orphan_spike.banner_emoji")
    spike_bar = "⚠═══════════════════════════════⚠"
    desc = f"""```ansi
\u001b[1;31m{spike_bar}
{f"{orphan_spike_emoji} BLOCK ORPHANED {orphan_spike_emoji}".center(len(spike_bar))}
{spike_bar}\u001b[0m
```

**{new_orphans} new orphan(s)** detected via Prometheus metrics

⚠️ Orphaned blocks represent lost mining revenue{revenue_line}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": f"{orphan_spike_emoji} Orphan Stats",
            "value": f"**New Orphans:** `{new_orphans}`\n**Total Orphans:** `{total_orphans}`",
            "inline": False
        },
        {
            "name": "🔧 Possible Causes",
            "value": "• Block propagation delay\n• Network latency to upstream nodes\n• Competing block found at same height\n• Check node connectivity and ZMQ health",
            "inline": False
        },
    ]

    return _embed(theme("orphan_spike.title"), desc, COLORS["red"], fields, footer=theme("orphan_spike.footer"))


def create_zmq_stale_embed(age_seconds, threshold=300, coin=None):
    """Sentinel alert: ZMQ messages are stale (no new block notifications)."""

    age_min = age_seconds / 60
    threshold_min = threshold / 60
    coin_str = f" ({coin})" if coin else ""

    desc = f"""```fix
{theme("infra.zmq_stale.banner")}
```

No ZMQ block notification received for **{age_min:.1f} minutes**{coin_str}

⚠️ {theme("infra.zmq_stale.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "📡 ZMQ Status",
            "value": f"**Last Message Age:** `{age_min:.1f} min` ({age_seconds:.0f}s)\n**Threshold:** `{threshold_min:.1f} min` ({threshold}s for {coin or 'default'})",
            "inline": False
        },
        {
            "name": "🔧 Check These",
            "value": f"• {coin or 'Node'} ZMQ endpoint running\n• ZMQ port connectivity\n• Node sync status\n• Stratum server ZMQ subscriber",
            "inline": False
        },
    ]

    return _embed(theme("infra.zmq_stale.title"), desc, COLORS["orange"], fields, footer=f"{theme('infra.zmq_stale.footer')}{coin_str}")


def create_worker_drop_embed(current, baseline_avg):
    """Sentinel alert: Active worker count dropped significantly."""

    drop_pct = ((baseline_avg - current) / baseline_avg * 100) if baseline_avg > 0 else 0

    desc = f"""```fix
{theme("worker_drop.banner")}
```

Active workers dropped from **{baseline_avg:.0f}** to **{current}** ({drop_pct:.0f}% decrease)

⚠️ {theme("worker_drop.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "👷 Worker Stats",
            "value": f"**Current Workers:** `{current}`\n**Baseline (avg):** `{baseline_avg:.0f}`\n**Drop:** `{drop_pct:.0f}%`",
            "inline": False
        },
        {
            "name": "🔧 Check These",
            "value": "• Miner connectivity\n• Pool stratum port\n• Network infrastructure\n• Power supply to miners",
            "inline": False
        },
    ]

    return _embed(theme("worker_drop.title"), desc, COLORS["orange"], fields, footer=theme("worker_drop.footer"))


def create_share_loss_embed(loss_rate_pct):
    """Sentinel alert: Share loss rate exceeds threshold."""

    desc = f"""```fix
{theme("share_loss.banner")}
```

Share batch loss rate is **{loss_rate_pct:.3f}%**

⚠️ {theme("share_loss.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "📉 Share Loss",
            "value": f"**Loss Rate:** `{loss_rate_pct:.3f}%`\n**Threshold:** `0.1%`",
            "inline": False
        },
        {
            "name": "🔧 Check These",
            "value": "• Pool database health\n• Stratum message integrity\n• Network packet loss\n• Disk I/O on pool server",
            "inline": False
        },
    ]

    return _embed(theme("share_loss.title"), desc, COLORS["orange"], fields, footer=theme("share_loss.footer"))


def create_notify_mode_embed(old_mode, new_mode):
    """Sentinel alert: Block notification mode changed (ZMQ <-> RPC polling)."""

    mode_names = {0: "RPC Polling", 1: "ZMQ (Real-time)"}
    old_name = mode_names.get(old_mode, f"Unknown ({old_mode})")
    new_name = mode_names.get(new_mode, f"Unknown ({new_mode})")

    # ZMQ -> Polling is a degradation, Polling -> ZMQ is recovery
    if new_mode == 0:
        severity = "DEGRADED"
        icon = "⚠️"
    else:
        severity = "RECOVERED"
        icon = "✅"

    desc = f"""```fix
{theme("notify_mode.banner")}
```

Block notification mode changed: **{old_name}** → **{new_name}**

{icon} **{severity}** - {'Falling back to slower polling' if new_mode == 0 else 'Restored to real-time ZMQ notifications'}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "📡 Mode Change",
            "value": f"**Previous:** `{old_name}`\n**Current:** `{new_name}`",
            "inline": False
        },
    ]

    color = COLORS["yellow"] if new_mode == 0 else COLORS["green"]
    return _embed(theme("notify_mode.title"), desc, color, fields, footer=theme("notify_mode.footer"))


def create_ha_replica_embed(old_count, new_count):
    """Sentinel alert: HA replica count decreased."""

    lost = old_count - new_count
    desc = f"**{lost}** replica node(s) lost. {theme('ha.replica_drop.body')}"

    fields = [
        {"name": "📊 Was",  "value": f"`{old_count}` replica(s)", "inline": True},
        {"name": "📊 Now",  "value": f"`{new_count}` replica(s)", "inline": True},
        {"name": "❌ Lost", "value": f"`{lost}` node(s)",         "inline": True},
        {
            "name": "🔧 Check These",
            "value": "• Replica node connectivity\n• Replica process status\n• DB replication health\n• `spiralpool ha status`",
            "inline": False
        },
    ]

    return _embed(theme("ha.replica_drop.title"), desc, COLORS["red"], fields, footer=theme("ha.replica_drop.footer"))


def create_ha_promoted_embed(node_ip, role, vip, reason=""):
    """Sentinel alert: Node promoted to MASTER - services starting."""

    hostname = _SENTINEL_HOSTNAME
    desc = f"**`{hostname}`** {theme('ha.promoted.body')}"

    fields = [
        {"name": "🖥️ Host",    "value": f"`{hostname}`",    "inline": True},
        {"name": "🌐 Node IP", "value": f"`{node_ip}`",     "inline": True},
        {"name": "📡 VIP",     "value": f"`{vip or 'N/A'}`","inline": True},
    ]
    if reason:
        fields.append({"name": "📋 Reason", "value": reason, "inline": False})

    return _embed(f"👑 Promoted to Master", desc, COLORS["green"], fields,
                  footer=f"🌀 {hostname} • {theme('ha.promoted.footer')}")


def create_ha_demoted_embed(node_ip, old_role, new_role, reason=""):
    """Sentinel alert: Node demoted to BACKUP - services stopping."""

    hostname = _SENTINEL_HOSTNAME
    desc = f"**`{hostname}`** {theme('ha.demoted.body')}"

    fields = [
        {"name": "🖥️ Host",       "value": f"`{hostname}`",                    "inline": True},
        {"name": "🌐 Node IP",    "value": f"`{node_ip}`",                     "inline": True},
        {"name": "🔀 Role Change","value": f"`{old_role}` → `{new_role}`",     "inline": True},
    ]
    if reason:
        fields.append({"name": "📋 Reason", "value": reason, "inline": False})

    return _embed(f"🔄 Demoted to {new_role}", desc, COLORS["yellow"], fields,
                  footer=f"🌀 {hostname} • {theme('ha.demoted.footer')}")


def create_ha_replication_lag_embed(lag_bytes, lag_seconds, threshold):
    """Sentinel alert: Database replication lag exceeds threshold."""

    lag_mb = lag_bytes / (1024 * 1024) if lag_bytes else 0

    desc = theme("ha.replication_lag.body")

    fields = [
        {"name": "📦 Lag",       "value": f"`{lag_mb:.1f} MB`",                            "inline": True},
        {"name": "⏱️ Delay",     "value": f"`{lag_seconds:.0f}s`",                          "inline": True},
        {"name": "📏 Threshold", "value": f"`{threshold / (1024 * 1024):.0f} MB`",          "inline": True},
        {
            "name": "⚠️ Impact",
            "value": "• Failover may result in data loss\n• Replica is behind the primary\n• Check network bandwidth and disk I/O",
            "inline": False
        },
    ]

    return _embed(theme("ha.replication_lag.title"), desc, COLORS["orange"], fields,
                  footer=f"🌀 Spiral Sentinel v{__version__} • {theme('ha.replication_lag.footer')}")


def create_ha_resync_embed(resync_type, progress_pct, eta_minutes, details=""):
    """Sentinel alert: Post-failover resync in progress."""

    eta_str = f"{eta_minutes:.0f} min" if eta_minutes and eta_minutes > 0 else "Estimating..."
    progress_bar = "█" * int(progress_pct / 10) + "░" * (10 - int(progress_pct / 10)) if progress_pct else "░" * 10
    desc = theme("ha.resync.body")

    fields = [
        {"name": "🔄 Type",     "value": f"`{resync_type}`",                    "inline": True},
        {"name": "📊 Progress", "value": f"`{progress_bar}` `{progress_pct:.0f}%`", "inline": True},
        {"name": "⏳ ETA",      "value": f"`{eta_str}`",                         "inline": True},
    ]
    if details:
        fields.append({
            "name": "📋 Details",
            "value": details,
            "inline": False
        })
    else:
        fields.append({
            "name": "📋 Resync Guidance",
            "value": "• **Database:** Patroni handles automatically via streaming replication (typically minutes)\n• **Blockchain:** P2P network sync — short outage (<1h) resyncs quickly\n• Extended outage may need manual `ha-replicate.sh`",
            "inline": False
        })

    return _embed(theme("ha.resync.title"), desc, COLORS["cyan"], fields,
                  footer=f"🌀 Spiral Sentinel v{__version__} • {theme('ha.resync.footer')}")


def create_circuit_breaker_embed(state_value):
    """Sentinel alert: Stratum circuit breaker triggered - pool is dropping shares."""
    state_names = {0: "CLOSED (normal)", 1: "OPEN (rejecting)", 2: "HALF-OPEN (testing)"}
    state_name = state_names.get(state_value, f"Unknown ({state_value})")

    cb_bar = "🔴═══════════════════════════════🔴"
    banner_text = theme("infra.circuit_breaker.banner")
    desc = f"""```ansi
\u001b[1;31m{cb_bar}
{banner_text:^{len(cb_bar)}}
{cb_bar}\u001b[0m
```

The stratum server circuit breaker is **{state_name}**

⚠️ **{theme("infra.circuit_breaker.body")}**

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🔴 Circuit Breaker State",
            "value": f"**State:** `{state_name}`\n**Impact:** Shares submitted during this period are **lost**",
            "inline": False
        },
        {
            "name": "🔧 Check These",
            "value": "• Pool database health and I/O\n• Disk space on pool server\n• Network connectivity to node\n• Check `spiralpool status` for details",
            "inline": False
        },
    ]

    return _embed(theme("infra.circuit_breaker.title"), desc, COLORS["red"], fields, footer=theme("infra.circuit_breaker.footer"))


def create_backpressure_embed(level, fill_pct):
    """Sentinel alert: Stratum backpressure at critical/emergency levels."""
    level_names = {0: "NONE", 1: "WARN", 2: "CRITICAL", 3: "EMERGENCY"}
    level_name = level_names.get(level, f"Unknown ({level})")

    if level >= 3:
        severity = "EMERGENCY - Buffer overflow imminent"
        icon = "🚨"
    else:
        severity = "CRITICAL - Buffer filling rapidly"
        icon = "⚠️"

    bp_bar = f"{icon}═══════════════════════════════{icon}"
    banner_text = theme("infra.backpressure.banner", level=level_name)
    desc = f"""```ansi
\u001b[1;31m{bp_bar}
{banner_text:^{len(bp_bar)}}
{bp_bar}\u001b[0m
```

Pool backpressure level: **{level_name}** (buffer {fill_pct:.0f}% full)

⚠️ **{theme("infra.backpressure.body")}**

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "📊 Buffer Status",
            "value": f"**Level:** `{level_name}`\n**Buffer Fill:** `{fill_pct:.0f}%`\n**Impact:** Shares may be dropped if buffer overflows",
            "inline": False
        },
        {
            "name": "🔧 Check These",
            "value": "• Pool database write speed\n• Disk I/O utilization\n• Share submission rate vs processing rate\n• Consider reducing connected miners temporarily",
            "inline": False
        },
    ]

    return _embed(f"{icon} {theme('infra.backpressure.title', level=level_name)}", desc, COLORS["red"], fields, footer=theme("infra.backpressure.footer"))


def create_wal_errors_embed(write_errors, commit_errors, delta_write, delta_commit):
    """Sentinel alert: WAL database write/commit failures detected."""

    total_delta = delta_write + delta_commit

    wal_bar = "💾═══════════════════════════════💾"
    banner_text = theme("infra.wal_errors.banner")
    desc = f"""```ansi
\u001b[1;31m{wal_bar}
{banner_text:^{len(wal_bar)}}
{wal_bar}\u001b[0m
```

**{total_delta} new database error(s)** detected

⚠️ {theme("infra.wal_errors.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "💾 WAL Error Counts",
            "value": f"**New Write Errors:** `{delta_write}`\n**New Commit Errors:** `{delta_commit}`\n**Total Write Errors:** `{write_errors}`\n**Total Commit Errors:** `{commit_errors}`",
            "inline": False
        },
        {
            "name": "🔧 Immediate Actions",
            "value": "• Check disk space (`df -h`)\n• Check disk I/O (`iostat`)\n• Review pool logs for database errors\n• Verify database file integrity\n• Consider restarting pool if persistent",
            "inline": False
        },
    ]

    return _embed(theme("infra.wal_errors.title"), desc, COLORS["red"], fields, footer=theme("infra.wal_errors.footer"))


def create_zmq_disconnected_embed(health_status, connected):
    """Sentinel alert: ZMQ socket disconnected or degraded."""
    status_names = {0: "Disabled", 1: "Connecting", 2: "Healthy", 3: "Degraded", 4: "Failed"}
    status_name = status_names.get(health_status, f"Unknown ({health_status})")

    zmq_bar = "📡═══════════════════════════════📡"
    banner_text = theme("infra.zmq_disconnected.banner")
    desc = f"""```ansi
\u001b[1;31m{zmq_bar}
{banner_text:^{len(zmq_bar)}}
{zmq_bar}\u001b[0m
```

ZMQ socket health: **{status_name}** | Connected: **{'Yes' if connected else 'No'}**

⚠️ {theme("infra.zmq_disconnected.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "📡 ZMQ Socket Status",
            "value": f"**Health:** `{status_name}`\n**Connected:** `{'Yes' if connected else 'No'}`\n**Impact:** Falling back to slower RPC polling for block detection",
            "inline": False
        },
        {
            "name": "🔧 Check These",
            "value": "• Node ZMQ endpoint (`zmqpubhashblock`)\n• ZMQ port open and accessible\n• Node process running\n• Stratum server ZMQ subscriber config",
            "inline": False
        },
    ]

    return _embed(theme("infra.zmq_disconnected.title"), desc, COLORS["red"], fields, footer=theme("infra.zmq_disconnected.footer"))


def create_stratum_url_mismatch_embed(name, expected_url, actual_url):
    """Sentinel alert: Miner stratum URL does not match expected configuration."""
    display_name = get_miner_display_name(name)
    miner_ip = _miner_ip_lookup.get(name, "unknown")

    url_bar = "🔒═══════════════════════════════🔒"
    banner_text = theme("url_mismatch.banner")
    desc = f"""```ansi
\u001b[1;31m{url_bar}
{banner_text:^{len(url_bar)}}
{url_bar}\u001b[0m
```

**{display_name}** is pointing to an unexpected pool!

⚠️ {theme("url_mismatch.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🔒 URL Comparison",
            "value": f"**Miner:** `{display_name}`\n**IP:** `{miner_ip}`\n**Expected:** `{expected_url}`\n**Actual:** `{actual_url}`",
            "inline": False
        },
        {
            "name": "⚠️ Investigate",
            "value": "• Check miner web UI for pool config\n• Verify firmware integrity (known hijack vector)\n• Check if failover pool activated\n• Re-flash firmware if tampering suspected",
            "inline": False
        },
    ]

    return _embed(theme("url_mismatch.title"), desc, COLORS["red"], fields, footer=theme("url_mismatch.footer"))


def create_hashboard_dead_embed(name, chain_hashrates, dead_chains):
    """Sentinel alert: One or more hashboards reporting 0 hashrate."""
    display_name = get_miner_display_name(name)

    chain_list = "\n".join(
        f"  Chain {i}: `{hr:.0f} GH/s` {'🔴 DEAD' if hr == 0 else '🟢'}"
        for i, hr in enumerate(chain_hashrates)
    )
    capacity_pct = ((len(chain_hashrates) - len(dead_chains)) / len(chain_hashrates) * 100) if chain_hashrates else 0

    hb_bar = "🪫═══════════════════════════════🪫"
    banner_text = theme("hashboard.banner")
    desc = f"""```ansi
\u001b[1;31m{hb_bar}
{banner_text:^{len(hb_bar)}}
{hb_bar}\u001b[0m
```

**{display_name}** has **{len(dead_chains)} dead hashboard(s)** — running at **{capacity_pct:.0f}%** capacity

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🪫 Per-Chain Hashrate",
            "value": f"**Miner:** `{display_name}`\n{chain_list}",
            "inline": False
        },
        {
            "name": "🔧 Required Action",
            "value": "• Power cycle the miner\n• Check hashboard cable connections\n• Inspect for blown fuses on dead board\n• Board replacement likely needed if persistent",
            "inline": False
        },
    ]

    return _embed(theme("hashboard.title"), desc, COLORS["red"], fields, footer=theme("hashboard.footer"))


def create_hw_error_rate_embed(name, errors_per_hour, total_errors):
    """Sentinel alert: Hardware error rate exceeding threshold."""
    display_name = get_miner_display_name(name)

    if errors_per_hour >= 100:
        severity = "SEVERE"
        bar = "🔴🔴🔴🔴🔴"
    elif errors_per_hour >= 50:
        severity = "HIGH"
        bar = "🔴🔴🔴🟡🟡"
    else:
        severity = "ELEVATED"
        bar = "🔴🔴🟡🟡🟡"

    desc = f"""```fix
{theme("hw_error.banner")}
```

**{display_name}** is producing **{errors_per_hour:.0f} HW errors/hour** {bar}

⚠️ **{severity}** - {theme("hw_error.body")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "⚠️ Error Stats",
            "value": f"**Miner:** `{display_name}`\n**Rate:** `{errors_per_hour:.0f}` errors/hour\n**Total Errors:** `{total_errors}`",
            "inline": False
        },
        {
            "name": "🔧 Likely Causes",
            "value": "• ASIC chip degradation (aging)\n• Overclocking too aggressive\n• Insufficient cooling\n• Power supply instability\n• Consider reducing frequency",
            "inline": False
        },
    ]

    return _embed(theme("hw_error.title"), desc, COLORS["orange"], fields, footer=theme("hw_error.footer"))


def create_best_share_embed(new_best, previous_best):
    """Sentinel alert: New all-time best share difficulty submitted."""

    # Format difficulty
    def fmt_diff(d):
        if d >= 1e15:
            return f"{d/1e15:.2f}P"
        elif d >= 1e12:
            return f"{d/1e12:.2f}T"
        elif d >= 1e9:
            return f"{d/1e9:.2f}G"
        elif d >= 1e6:
            return f"{d/1e6:.2f}M"
        elif d >= 1e3:
            return f"{d/1e3:.2f}K"
        else:
            return f"{d:,.0f}"

    improvement = ((new_best - previous_best) / previous_best * 100) if previous_best > 0 else 0

    best_share_flavor = theme("best_share.flavor")
    desc = f"""```fix
NEW BEST SHARE
```

{best_share_flavor}

🏆 Difficulty: **{fmt_diff(new_best)}**

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🏆 Share Record",
            "value": f"**New Best:** `{fmt_diff(new_best)}`\n**Previous Best:** `{fmt_diff(previous_best)}`\n**Improvement:** `{improvement:.0f}%`",
            "inline": False
        },
    ]

    return _embed(theme("best_share.title"), desc, COLORS["purple"], fields, footer=theme("best_share.footer"))


def create_power_event_embed(i):
    """Sentinel alert: Fleet-wide power event - ENHANCED"""
    miners_list = "\n".join([f"• {m}" for m in i['miners']])
    count = i['count']

    # Severity based on count
    if count >= 4:
        severity_bar = "⚡⚡⚡⚡⚡"
        severity = "MAJOR OUTAGE"
    elif count >= 2:
        severity_bar = "⚡⚡⚡⚪⚪"
        severity = "PARTIAL OUTAGE"
    else:
        severity_bar = "⚡⚡⚪⚪⚪"
        severity = "MINOR EVENT"

    pwr_bar = "⚠═══════════════════════════════⚠"
    desc = f"""```ansi
\u001b[1;31m{pwr_bar}
{"⚡ POWER EVENT ⚡".center(len(pwr_bar))}
{pwr_bar}\u001b[0m
```

**{severity}** - Multiple rigs rebooted simultaneously

{severity_bar}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "⚡ Impact Summary",
            "value": f"**Affected Miners:** `{count}`\n**Severity:** {severity}",
            "inline": False
        },
        {
            "name": "🖥️ Affected Rigs",
            "value": miners_list,
            "inline": False
        },
        {
            "name": "🔌 Possible Causes",
            "value": "• Power grid fluctuation\n• UPS failure\n• Circuit overload",
            "inline": False
        },
    ]

    return _embed(theme("power_event.title"), desc, COLORS["red"], fields, footer=theme("power_event.footer"))


def create_opportunity_embed(opportunity_type, net_phs, odds_pct=None, days_per_block=None,
                             previous_phs=None, drop_pct=None,
                             fleet_ths=None, coin_symbol=None):
    """Unified opportunity alert embed - consolidates HIGH_ODDS and CRASH alerts.

    opportunity_type: "HIGH_ODDS" | "CRASH"
    - HIGH_ODDS: Network is low, good mining conditions
    - CRASH: Network dropped suddenly (includes drop_pct, previous_phs)
    """
    coin = coin_symbol.upper() if coin_symbol else (get_primary_coin() or "UNKNOWN")
    lv, em = get_status_level(net_phs, coin)
    coin_emoji = get_coin_emoji(coin)

    fields = []

    # Type-specific fields and styling
    if opportunity_type == "CRASH" and previous_phs and drop_pct:
        # Crash severity visualization
        if drop_pct >= 30:
            severity_bar = "📉📉📉📉📉"
            severity = "MASSIVE"
        elif drop_pct >= 20:
            severity_bar = "📉📉📉📉⚪"
            severity = "MAJOR"
        elif drop_pct >= 10:
            severity_bar = "📉📉📉⚪⚪"
            severity = "SIGNIFICANT"
        else:
            severity_bar = "📉📉⚪⚪⚪"
            severity = "NOTABLE"

        crash_banner = theme("opportunity.crash.banner")
        crash_flavor = theme("opportunity.crash.flavor", severity=severity)
        title = f"{coin_emoji} {theme('opportunity.crash.title')}"
        crash_bar = "⚠═══════════════════════════════⚠"
        desc = f"""```ansi
\u001b[1;31m{crash_bar}
{crash_banner.center(len(crash_bar))}
{crash_bar}\u001b[0m
```

{crash_flavor}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

        # Network stats in a cleaner format
        fields.append({
            "name": "📊 Network Status",
            "value": f"**Before:** `{previous_phs:.2f} PH/s`\n**After:** `{net_phs:.2f} PH/s`\n**Drop:** `{drop_pct:.1f}%` {severity_bar}",
            "inline": False
        })

        color = COLORS["cyan"]
        footer = theme("opportunity.crash.footer", coin=coin, drop_pct=drop_pct)

    else:  # HIGH_ODDS (default)
        # Odds visualization bar
        if odds_pct and odds_pct >= 5:
            odds_bar = "🎰🎰🎰🎰🎰"
            odds_status = "JACKPOT"
        elif odds_pct and odds_pct >= 2:
            odds_bar = "🎰🎰🎰🎰⚪"
            odds_status = "EXCELLENT"
        elif odds_pct and odds_pct >= 1:
            odds_bar = "🎰🎰🎰⚪⚪"
            odds_status = "GREAT"
        else:
            odds_bar = "🎰🎰⚪⚪⚪"
            odds_status = "GOOD"

        high_odds_banner = theme("opportunity.high_odds.banner")
        high_odds_flavor = theme("opportunity.high_odds.flavor", odds_status=odds_status)
        odds_bar = "★═══════════════════════════════★"
        title = f"{coin_emoji} {theme('opportunity.high_odds.title')}"
        desc = f"""```ansi
\u001b[1;32m{odds_bar}
{high_odds_banner.center(len(odds_bar))}
{odds_bar}\u001b[0m
```

{high_odds_flavor}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

        # Network status
        fields.append({
            "name": "🌐 Network",
            "value": f"**Hashrate:** `{net_phs:.2f} PH/s`\n**Status:** {em} {lv}",
            "inline": False
        })

        # Odds and timing info
        odds_lines = []
        if odds_pct:
            odds_lines.append(f"**Daily Odds:** `{odds_pct:.1f}%` {odds_bar}")
        if days_per_block and days_per_block != float('inf') and days_per_block < 99999:
            odds_lines.append(f"**Est. Days/Block:** `~{days_per_block:.1f} days`")
        if odds_lines:
            fields.append({
                "name": "🎰 Your Chances",
                "value": "\n".join(odds_lines),
                "inline": False
            })

        color = COLORS["green"]
        footer = theme("opportunity.high_odds.footer", coin=coin, time=local_now().strftime('%H:%M'))

    return _embed(title, desc, color, fields, footer=footer)


def create_high_odds_embed(odds_pct, net_phs, days_per_block, coin_symbol=None):
    """Sentinel alert: Prime mining conditions detected! - WRAPPER

    Uses consolidated create_opportunity_embed for consistency.
    """
    return create_opportunity_embed(
        opportunity_type="HIGH_ODDS",
        net_phs=net_phs,
        odds_pct=odds_pct,
        days_per_block=days_per_block,
        coin_symbol=coin_symbol
    )


def create_hashrate_crash_embed(current_phs, previous_phs, drop_pct, coin_symbol=None):
    """Sentinel alert: Network hashrate crash - hunting window opened! - WRAPPER

    Uses consolidated create_opportunity_embed for consistency.
    """
    return create_opportunity_embed(
        opportunity_type="CRASH",
        net_phs=current_phs,
        previous_phs=previous_phs,
        drop_pct=drop_pct,
        coin_symbol=coin_symbol
    )


def create_pool_hashrate_drop_embed(current_ths, expected_ths, drop_pct, miner_details):
    """Sentinel alert: Pool hashrate dropped significantly - ENHANCED

    📉 POOL HASHRATE DROP
    ⚠️ Pool hashrate dropped 50.5%
    """
    # Severity visualization
    if drop_pct >= 50:
        drop_bar = "🔴🔴🔴🔴🔴"
        severity = "CRITICAL"
    elif drop_pct >= 30:
        drop_bar = "🟠🟠🟠🟠⚪"
        severity = "SEVERE"
    elif drop_pct >= 15:
        drop_bar = "🟡🟡🟡⚪⚪"
        severity = "MODERATE"
    else:
        drop_bar = "🟡🟡⚪⚪⚪"
        severity = "MINOR"

    desc = f"""```diff
- {theme("pool_drop.banner")}
```

{theme("pool_drop.body", severity=severity, drop_pct=drop_pct)}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = []

    # Hashrate comparison in a cleaner format
    fields.append({
        "name": "📊 Hashrate Status",
        "value": f"**Current:** `{current_ths:.2f} TH/s`\n**Expected:** `{expected_ths:.2f} TH/s`\n**Drop:** `{drop_pct:.1f}%` {drop_bar}",
        "inline": False
    })

    # Add miner details with enhanced formatting
    if miner_details:
        online_rigs = []
        offline_rigs = []
        for name, info in miner_details.items():
            hr = info.get("hashrate", "?")
            power = info.get("power", 0)
            power_str = f" ⚡ `{power:.0f}W`" if power > 0 else ""
            if info.get("online"):
                online_rigs.append(f"🟢 **{name}**\n    └ `{hr}`{power_str}")
            else:
                offline_rigs.append(f"🔴 **{name}**\n    └ OFFLINE")

        # Show offline rigs first (they're the problem)
        rig_lines = offline_rigs + online_rigs
        rig_display = "\n".join(rig_lines)
        fields.append({
            "name": f"🖥️ Fleet Status ({len(online_rigs)} Online / {len(offline_rigs)} Offline)",
            "value": rig_display if rig_display else "No miner data",
            "inline": False
        })

    return _embed(theme("pool_drop.title"), desc, COLORS["orange"], fields, footer=theme("pool_drop.footer"))


def create_weekly_embed(stats, uptime=None, health_scores=None, earnings=None):
    """Enhanced weekly summary with visual formatting."""
    fields = []

    # Averages section with code block
    avg_val = f"```yaml\nNetwork:  {stats.get('avg_network_phs',0):.1f} PH/s\nFleet:    {stats.get('avg_fleet_ths',0):.1f} TH/s\nOdds:     {stats.get('avg_odds',0):.1f}%\n```"
    fields.append({"name": "📊 AVERAGES", "value": avg_val, "inline": True})

    # Events section with visual indicators
    blocks = stats.get('blocks_found', 0)
    offline = stats.get('offline_events', 0)
    blocks_display = f"```diff\n+ {blocks} blocks\n```" if blocks > 0 else "```\n0 blocks\n```"
    offline_display = f"🔴 {offline} offline events" if offline > 0 else "🟢 No offline events"
    fields.append({"name": "📋 EVENTS", "value": f"{blocks_display}{offline_display}", "inline": True})

    # Uptime section with health bars
    if uptime:
        uptime_lines = []
        for n, p in sorted(uptime.items(), key=lambda x: x[1], reverse=True):
            if p >= 99.9:
                bar = "🟢🟢🟢🟢🟢"
            elif p >= 99:
                bar = "🟢🟢🟢🟢⚪"
            elif p >= 95:
                bar = "🟡🟡🟡⚪⚪"
            elif p >= 90:
                bar = "🟠🟠⚪⚪⚪"
            else:
                bar = "🔴⚪⚪⚪⚪"
            uptime_lines.append(f"{bar} **{n}**: `{p:.1f}%`")
        fields.append({"name": "⏱️ UPTIME", "value": "\n".join(uptime_lines), "inline": False})

    # Earnings section - multi-coin support
    if earnings:
        # Build earnings display for all coins with non-zero earnings
        coins_earned = []
        if earnings.get('dgb', 0) > 0:
            coins_earned.append(f"+ {earnings['dgb']:.2f} DGB")
        if earnings.get('btc', 0) > 0:
            coins_earned.append(f"+ {earnings['btc']:.8f} BTC")
        if earnings.get('bch', 0) > 0:
            coins_earned.append(f"+ {earnings['bch']:.8f} BCH")
        if earnings.get('bc2', 0) > 0:
            coins_earned.append(f"+ {earnings['bc2']:.2f} BC2")
        if earnings.get('nmc', 0) > 0:
            coins_earned.append(f"+ {earnings['nmc']:.8f} NMC")
        if earnings.get('sys', 0) > 0:
            coins_earned.append(f"+ {earnings['sys']:.8f} SYS")
        if earnings.get('xmy', 0) > 0:
            coins_earned.append(f"+ {earnings['xmy']:.2f} XMY")
        if earnings.get('fbtc', 0) > 0:
            coins_earned.append(f"+ {earnings['fbtc']:.8f} FBTC")
        if earnings.get('ltc', 0) > 0:
            coins_earned.append(f"+ {earnings['ltc']:.8f} LTC")
        if earnings.get('doge', 0) > 0:
            coins_earned.append(f"+ {earnings['doge']:.2f} DOGE")
        if earnings.get('dgb-scrypt', 0) > 0:
            coins_earned.append(f"+ {earnings['dgb-scrypt']:.2f} DGB-SCRYPT")
        if earnings.get('pep', 0) > 0:
            coins_earned.append(f"+ {earnings['pep']:.2f} PEP")
        if earnings.get('cat', 0) > 0:
            coins_earned.append(f"+ {earnings['cat']:.2f} CAT")

        if coins_earned:
            earnings_val = f"```diff\n" + "\n".join(coins_earned) + "\n```"
        else:
            earnings_val = "```\nNo earnings this period\n```"

        cur = get_currency_meta()
        fiat_val = earnings.get(cur["code"], 0)
        earnings_val += f"{cur['emoji']} {cur['symbol']}{fiat_val:,.{cur['decimals']}f} {REPORT_CURRENCY}"
        fields.append({"name": "💰 EARNINGS", "value": earnings_val, "inline": True})

    # Weekly electricity cost
    power_data = fetch_power_cost()
    if power_data and not power_data.get("is_free_power"):
        sym = power_data.get("currency_symbol", "$")
        weekly_cost = power_data["daily_cost"] * 7
        weekly_kwh = power_data["daily_kwh"] * 7
        weekly_profit = power_data.get("daily_profit", 0) * 7
        pv = f"```yaml\nWeekly:  {sym}{weekly_cost:.2f} ({weekly_kwh:.0f} kWh)\nMonthly: {sym}{power_data['monthly_cost']:.2f}\n```"
        if weekly_profit >= 0:
            pv += f"📈 Weekly net: **+{sym}{weekly_profit:.2f}**"
        else:
            pv += f"📉 Weekly net: **{sym}{weekly_profit:.2f}**"
        fields.append({"name": "⚡ POWER COST", "value": pv, "inline": True})
    elif power_data and power_data.get("is_free_power"):
        fields.append({"name": "⚡ POWER COST", "value": "```diff\n+ FREE POWER\n```", "inline": True})

    weekly_bar = "═" * 27
    weekly_banner = theme("weekly.banner")
    desc = f"""```ansi
\u001b[1;34m{weekly_bar}
{weekly_banner.center(len(weekly_bar))}
{weekly_bar}\u001b[0m
```
📅 Week ending **{local_now().strftime('%Y-%m-%d')}**"""

    return _embed(theme("weekly.title"), desc, COLORS["blue"], fields, footer=theme("weekly.footer", version=__version__))

def create_monthly_earnings_embed(earnings, prices, wallet_balance=None, coin_symbol=None):
    """Create monthly earnings report with multi-coin support - ENHANCED."""
    fields = []
    # Get primary coin for wallet display
    primary_coin = coin_symbol or get_primary_coin() or "UNKNOWN"

    # Multi-coin earnings display with code blocks - ALL 14 coins (alphabetically ordered)
    coins_mined = []
    # SHA-256d coins (alphabetically)
    if earnings.get('bc2', 0) > 0:
        coins_mined.append(f"🔷 {earnings['bc2']:.2f} BC2")
    if earnings.get('bch', 0) > 0:
        coins_mined.append(f"🟢 {earnings['bch']:.8f} BCH")
    if earnings.get('btc', 0) > 0:
        coins_mined.append(f"🟠 {earnings['btc']:.8f} BTC")
    # Scrypt coins (alphabetically)
    if earnings.get('cat', 0) > 0:
        coins_mined.append(f"🐱 {earnings['cat']:.2f} CAT")
    if earnings.get('dgb', 0) > 0:
        coins_mined.append(f"💎 {earnings['dgb']:.2f} DGB")
    if earnings.get('dgb-scrypt', 0) > 0:
        coins_mined.append(f"💎 {earnings['dgb-scrypt']:.2f} DGB-SCRYPT")
    if earnings.get('doge', 0) > 0:
        coins_mined.append(f"🐶 {earnings['doge']:.2f} DOGE")
    if earnings.get('fbtc', 0) > 0:
        coins_mined.append(f"🔶 {earnings['fbtc']:.8f} FBTC")
    if earnings.get('ltc', 0) > 0:
        coins_mined.append(f"⚡ {earnings['ltc']:.8f} LTC")
    if earnings.get('nmc', 0) > 0:
        coins_mined.append(f"🌐 {earnings['nmc']:.8f} NMC")
    if earnings.get('pep', 0) > 0:
        coins_mined.append(f"🐸 {earnings['pep']:.2f} PEP")
    if earnings.get('sys', 0) > 0:
        coins_mined.append(f"⚙️ {earnings['sys']:.8f} SYS")
    if earnings.get('xmy', 0) > 0:
        coins_mined.append(f"🔵 {earnings['xmy']:.2f} XMY")

    if coins_mined:
        coins_val = "```diff\n+ " + "\n+ ".join(coins_mined) + "\n```"
        fields.append({"name": "⛏️ COINS MINED", "value": coins_val, "inline": True})
    else:
        fields.append({"name": "⛏️ COINS MINED", "value": "```\nNo blocks this month\n```", "inline": True})

    # Block count with celebration
    blocks = earnings.get('blocks', 0)
    if blocks > 0:
        block_bar = "🏆" * min(blocks, 5)
        fields.append({"name": "🏆 BLOCKS", "value": f"```fix\n{blocks} blocks!\n```{block_bar}", "inline": True})

    # Total value with currency preference
    value_val = format_value_block(earnings)
    fields.append({"name": "💰 VALUE", "value": value_val, "inline": True})

    # Wallet balance
    if wallet_balance is not None and prices:
        wallet_val = f"```yaml\nBalance: {wallet_balance:,.2f} {primary_coin}\n```"
        wallet_val += format_wallet_fiat(wallet_balance, prices)
        fields.append({"name": "🏦 WALLET", "value": wallet_val, "inline": True})

    # Monthly maintenance reminder
    maint_tasks = [
        "🔄 Update system packages",
        "🗑️ Clear old logs",
        "💾 Verify backups",
        "🔍 Check disk space",
        "📊 Review pool performance",
    ]
    maint_val = "```\n" + "\n".join(f"☐ {t}" for t in maint_tasks) + "\n```"
    fields.append({"name": "🔧 MONTHLY MAINTENANCE", "value": maint_val, "inline": False})

    # Monthly electricity cost
    power_data = fetch_power_cost()
    if power_data and not power_data.get("is_free_power"):
        sym = power_data.get("currency_symbol", "$")
        margin = power_data.get("profit_margin_percent", 0)
        pv = f"```yaml\nMonthly Cost: {sym}{power_data['monthly_cost']:.2f} ({power_data['monthly_kwh']:.0f} kWh)\n```"
        net = power_data.get("monthly_profit", 0)
        if net >= 0:
            pv += f"📈 Net profit: **+{sym}{net:.2f}** ({margin:.0f}% margin)"
        else:
            pv += f"📉 Net loss: **{sym}{net:.2f}** ({margin:.0f}% margin)"
        fields.append({"name": "⚡ ELECTRICITY", "value": pv, "inline": True})
    elif power_data and power_data.get("is_free_power"):
        fields.append({"name": "⚡ ELECTRICITY", "value": "```diff\n+ FREE POWER\n```💰 100% profit margin", "inline": True})

    # Monthly description
    month_name = local_now().strftime('%B %Y')
    monthly_bar = "═" * 27
    monthly_banner = theme("monthly.banner")
    desc = f"""```ansi
\u001b[1;33m{monthly_bar}
{monthly_banner.center(len(monthly_bar))}
{monthly_bar}\u001b[0m
```
📅 **{month_name}**"""

    return _embed(theme("monthly.title"), desc, COLORS["gold"], fields, footer=theme("monthly.footer", version=__version__))

def create_maintenance_reminder_embed():
    """Create monthly maintenance reminder embed.

    Sent on the 1st of each month at 8am to remind users to perform maintenance.
    Includes information about maintenance mode suspension.
    """
    month_name = local_now().strftime("%B %Y")

    maint_bar = "═" * 34
    maint_banner = theme("maintenance.banner")
    desc = f"""```ansi
\u001b[1;33m{maint_bar}
{maint_banner.center(len(maint_bar))}
{maint_bar}\u001b[0m
```
📅 **{month_name}** - {theme("maintenance.intro")}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = []

    # Maintenance checklist
    checklist = """```diff
+ Clean dust filters and fans
+ Check miner temperatures
+ Verify all connections
+ Update firmware if needed
+ Check PSU voltages
+ Inspect for wear/damage
+ Clean intake/exhaust vents
+ Test backup systems
```"""
    fields.append({"name": "📋 MAINTENANCE CHECKLIST", "value": checklist, "inline": False})

    # Maintenance mode instructions
    maint_mode = """Before starting maintenance, **suspend Sentinel alerts** to avoid false alarms:

```bash
# Suspend for 1 hour (adjust as needed)
spiralpool-maintenance enable 60

# Suspend for 2 hours
spiralpool-maintenance enable 120

# Custom duration with reason
spiralpool-maintenance enable 90 "PSU swap"

# Check status
spiralpool-maintenance status

# Extend if needed
spiralpool-maintenance extend 30

# Resume early when done
spiralpool-maintenance disable
```

⏱️ **Choose duration based on your fleet size** - each user takes different time."""
    fields.append({"name": "⏸️ MAINTENANCE MODE", "value": maint_mode, "inline": False})

    # Pro tips
    tips = """• Document any changes made
• Keep spare parts on hand
• Schedule during low-value periods
• Consider staggering large fleets"""
    fields.append({"name": "💡 PRO TIPS", "value": tips, "inline": False})

    return _embed(theme("maintenance.title"), desc, COLORS["orange"], fields,
                  footer=theme("maintenance.footer", version=__version__))

def create_quarterly_embed(stats, trends, prices, wallet_balance=None, coin_symbol=None):
    """Create quarterly summary report with multi-coin support"""
    fields = []
    quarter = get_quarter_name()
    # Get primary coin for wallet display
    primary_coin = coin_symbol or get_primary_coin() or "UNKNOWN"

    # Performance section with multi-coin - ALL 14 coins (alphabetically ordered)
    perf = f"🏆 Blocks: **{stats.get('total_blocks', 0)}**\n"

    # Show each coin mined - SHA-256d coins (alphabetically)
    if stats.get('total_bc2', 0) > 0:
        perf += f"🔷 **{stats.get('total_bc2', 0):,.1f} BC2**\n"
    if stats.get('total_bch', 0) > 0:
        perf += f"🟢 **{stats.get('total_bch', 0):.8f} BCH**\n"
    if stats.get('total_btc', 0) > 0:
        perf += f"🟠 **{stats.get('total_btc', 0):.8f} BTC**\n"
    # Scrypt coins (alphabetically)
    if stats.get('total_cat', 0) > 0:
        perf += f"🐱 **{stats.get('total_cat', 0):,.1f} CAT**\n"
    if stats.get('total_dgb', 0) > 0:
        perf += f"💎 **{stats.get('total_dgb', 0):,.1f} DGB**\n"
    if stats.get('total_dgb-scrypt', 0) > 0:
        perf += f"💎 **{stats.get('total_dgb-scrypt', 0):,.1f} DGB-SCRYPT**\n"
    if stats.get('total_doge', 0) > 0:
        perf += f"🐶 **{stats.get('total_doge', 0):,.1f} DOGE**\n"
    if stats.get('total_fbtc', 0) > 0:
        perf += f"🔶 **{stats.get('total_fbtc', 0):.8f} FBTC**\n"
    if stats.get('total_ltc', 0) > 0:
        perf += f"⚡ **{stats.get('total_ltc', 0):.8f} LTC**\n"
    if stats.get('total_nmc', 0) > 0:
        perf += f"🌐 **{stats.get('total_nmc', 0):.8f} NMC**\n"
    if stats.get('total_pep', 0) > 0:
        perf += f"🐸 **{stats.get('total_pep', 0):,.1f} PEP**\n"
    if stats.get('total_sys', 0) > 0:
        perf += f"⚙️ **{stats.get('total_sys', 0):.8f} SYS**\n"
    if stats.get('total_xmy', 0) > 0:
        perf += f"🔵 **{stats.get('total_xmy', 0):,.1f} XMY**\n"

    # Calculate total fiat value using all_prices — preferred currency
    all_prices = fetch_all_prices() or {}
    total_fiat = compute_portfolio_total(stats, all_prices)
    if total_fiat > 0:
        cur = get_currency_meta()
        perf += f"{cur['emoji']} **{cur['symbol']}{total_fiat:,.{cur['decimals']}f} {REPORT_CURRENCY}**"

    fields.append({"name": "📊 Quarterly Performance", "value": perf, "inline": True})

    # Trend analysis
    if trends:
        diff_trend = trends.get("difficulty", {})
        net_trend = trends.get("network_phs", {})
        trend_emoji = {"rising": "📈", "falling": "📉", "flat": "➡️"}

        tv = f"🎯 Difficulty: {trend_emoji.get(diff_trend.get('trend', 'flat'), '➡️')} {diff_trend.get('trend', 'N/A')}\n"
        tv += f"🌐 Network: {trend_emoji.get(net_trend.get('trend', 'flat'), '➡️')} {net_trend.get('pct_change', 0):+.2f}%\n"
        if diff_trend.get('ema'):
            tv += f"📊 EMA: {format_difficulty(diff_trend['ema'])}"
        fields.append({"name": "📈 Trends", "value": tv, "inline": True})

    # Uptime summary
    if stats.get('avg_uptime'):
        uv = f"⏱️ Avg Uptime: **{stats['avg_uptime']:.1f}%**\n"
        uv += f"🔴 Offline Events: {stats.get('offline_events', 0)}"
        fields.append({"name": "⏱️ Reliability", "value": uv, "inline": True})

    # Wallet (uses user's currency preference)
    if wallet_balance is not None and prices:
        wv = f"💰 {wallet_balance:,.2f} {primary_coin}\n"
        wv += format_wallet_fiat(wallet_balance, prices)
        fields.append({"name": "🏦 Wallet Balance", "value": wv, "inline": True})

    # Quarterly electricity cost
    power_data = fetch_power_cost()
    if power_data and not power_data.get("is_free_power"):
        sym = power_data.get("currency_symbol", "$")
        quarterly_cost = power_data["monthly_cost"] * 3
        quarterly_profit = power_data.get("monthly_profit", 0) * 3
        margin = power_data.get("profit_margin_percent", 0)
        pv = f"```yaml\nQuarterly Cost: {sym}{quarterly_cost:.2f}\n```"
        if quarterly_profit >= 0:
            pv += f"📈 Quarterly net: **+{sym}{quarterly_profit:.2f}** ({margin:.0f}% margin)"
        else:
            pv += f"📉 Quarterly net: **{sym}{quarterly_profit:.2f}** ({margin:.0f}% margin)"
        fields.append({"name": "⚡ ELECTRICITY", "value": pv, "inline": True})
    elif power_data and power_data.get("is_free_power"):
        fields.append({"name": "⚡ ELECTRICITY", "value": "```diff\n+ FREE POWER\n```", "inline": True})

    return _embed(theme("quarterly.title", quarter=quarter), theme("quarterly.body", date=local_now().strftime('%Y-%m-%d')), COLORS["purple"], fields)

def create_special_date_embed(special_info, stats, trends):
    """Create special date (equinox/solstice) report"""
    emoji = special_info.get("emoji", "🌟")
    name = special_info.get("name", "Special Day")
    date_type = special_info.get("type", "special")

    fields = []

    # Current status
    if stats:
        sv = f"🌐 Network: **{stats.get('network_phs', 0):.2f} PH/s**\n"
        sv += f"⛏️ Fleet: **{stats.get('fleet_ths', 0):.2f} TH/s**\n"
        sv += f"🎰 Odds: **{stats.get('daily_odds', 0):.1f}%**"
        fields.append({"name": f"{emoji} Current Status", "value": sv, "inline": True})

    # Yearly trends (if we have data)
    if trends:
        trend_30d = trends.get("30d", {})
        if trend_30d:
            trend_emoji = {"rising": "📈", "falling": "📉", "flat": "➡️"}
            tv = f"30-Day Trend: {trend_emoji.get(trend_30d.get('trend', 'flat'), '➡️')}\n"
            tv += f"Avg: {trend_30d.get('avg', 0):.2f} PH/s\n"
            tv += f"Change: {trend_30d.get('pct_change', 0):+.2f}%"
            fields.append({"name": "📊 Monthly Trend", "value": tv, "inline": True})

    # Thematic message based on specific holiday/event
    theme_key = special_info.get("theme_key", "fallback")
    msg = theme(f"special.{theme_key}")
    if msg.startswith("special."):
        msg = theme("special.fallback")
    fields.append({"name": "🌟 Mining Wisdom", "value": msg, "inline": False})

    return _embed(theme("special.title", emoji=emoji, name=name), f"*{local_now().strftime('%Y-%m-%d')}*", COLORS["purple"], fields)

def create_consolidated_report_embed(report_types, data):
    """Create a consolidated report when multiple reports are due at the same time.

    Args:
        report_types: list of report type strings (e.g., ["6h", "weekly", "monthly", "quarterly", "special"])
        data: dict containing all needed data for reports

    Returns: single consolidated embed
    """
    fields = []
    now = local_now()

    # Build title based on report types
    type_labels = {
        "6h": "6-HOUR",
        "weekly": "WEEKLY",
        "monthly": "MONTHLY",
        "quarterly": "QUARTERLY",
        "special": (data.get("special_info") or {}).get("name", "SPECIAL").upper()
    }
    title_parts = [type_labels.get(t, t.upper()) for t in report_types]
    title = " + ".join(title_parts) + " " + theme("consolidated.title_suffix")

    # Add special date emoji if applicable
    special_emoji = (data.get("special_info") or {}).get("emoji", "")
    if special_emoji:
        title = f"{special_emoji} {title}"
    else:
        title = f"📊 {title}"

    # Color: use purple for special dates, gold for quarterly, blue for weekly, green for regular
    if "special" in report_types:
        color = COLORS["purple"]
    elif "quarterly" in report_types:
        color = COLORS.get("purple", COLORS["gold"])
    elif "monthly" in report_types:
        color = COLORS["gold"]
    elif "weekly" in report_types:
        color = COLORS["blue"]
    else:
        active_coin_for_color = data.get("coin_symbol")
        active_coin_for_color = active_coin_for_color.upper() if active_coin_for_color else None
        lv, _ = get_status_level(data.get("net_phs", 50), active_coin_for_color)
        color = COLORS["green"] if lv in ["AMAZING","GREAT"] else COLORS["blue"] if lv=="GOOD" else COLORS["yellow"]

    # === NETWORK SECTION (always include) ===
    net_phs = data.get("net_phs", 0)
    fleet_ths = data.get("fleet_ths", 0)
    odds = data.get("odds", {})
    diff = data.get("diff", 0)
    extremes = data.get("extremes", {})
    trends = data.get("trends", {})
    active_coin = data.get("coin_symbol")
    active_coin = active_coin.upper() if active_coin else None

    lv, em = get_status_level(net_phs, active_coin)
    # Use coin-specific volatility threshold for meaningful trends
    volatility_threshold = get_coin_volatility_threshold(active_coin)
    trend_arrow = "➡️"
    if trends.get("network", 0) > volatility_threshold: trend_arrow = "📈"
    elif trends.get("network", 0) < -volatility_threshold: trend_arrow = "📉"

    # Unified formatting with create_report_embed - use code block and helper functions
    net_status = "+" if lv in ["AMAZING", "GREAT", "GOOD"] else "-" if lv == "BAD" else " "
    net_hashrate_str = format_hashrate_phs(net_phs, symbol=active_coin)
    nv = f"```diff\n{net_status} {net_hashrate_str}  {lv}\n```"
    nv += f"{em} {trend_arrow}"
    if diff: nv += f"\n🎯 Diff: `{format_difficulty(diff)}`"
    if extremes.get("best"):
        best_str = format_hashrate_phs(extremes['best']['phs'], symbol=active_coin)
        nv += f"\n🏆 Best: **{best_str}**"
        if extremes['best'].get('time'): nv += f" `{extremes['best']['time']}`"
    if extremes.get("worst"):
        worst_str = format_hashrate_phs(extremes['worst']['phs'], symbol=active_coin)
        nv += f"\n📈 Peak: **{worst_str}**"
        if extremes['worst'].get('time'): nv += f" `{extremes['worst']['time']}`"
    fields.append({"name": "📡 NETWORK", "value": nv, "inline": False})

    # === DIFFICULTY TRENDS SECTION (if available) ===
    diff_trends = data.get("diff_trends", {})
    if diff_trends:
        trend_emoji_map = {"rising": "📈", "falling": "📉", "flat": "➡️"}
        coin_emoji = get_coin_emoji(active_coin)
        algo_note = f" ({get_coin_algorithm(active_coin).upper()})" if active_coin else ""
        coin_label = active_coin or "Network"
        tv = f"**{coin_label} Difficulty{algo_note}**\n"

        for label, key in [("6h", "6h"), ("12h", "12h"), ("24h", "1d"), ("7d", "7d"), ("30d", "30d")]:
            d = diff_trends.get(key)
            if d and d.get("samples", 0) >= 2:
                d_arrow = trend_emoji_map.get(d.get("trend", "flat"), "➡️")
                tv += f"{label}: {d_arrow} {d.get('pct_change', 0):+.2f}%\n"
            else:
                tv += f"{label}: `N/A`\n"

        tv += f"*±{volatility_threshold}% threshold*"
        fields.append({"name": f"{coin_emoji} Trends", "value": tv, "inline": True})

    # === AUX CHAIN DIFFICULTY TRENDS (merge mining) ===
    aux_diff_trends_data = data.get("aux_diff_trends", {})
    if aux_diff_trends_data:
        trend_emoji_aux = {"rising": "📈", "falling": "📉", "flat": "➡️"}
        for aux_coin, aux_trends in aux_diff_trends_data.items():
            aux_emoji = get_coin_emoji(aux_coin)
            aux_algo = get_coin_algorithm(aux_coin)
            aux_algo_label = "SCRYPT" if aux_algo == "scrypt" else "SHA-256D"
            aux_vt = get_coin_volatility_threshold(aux_coin)
            atv = f"**{aux_coin} Difficulty ({aux_algo_label})**\n"
            for label, key in [("6h", "6h"), ("12h", "12h"), ("24h", "1d"), ("7d", "7d"), ("30d", "30d")]:
                d = aux_trends.get(key)
                if d and d.get("samples", 0) >= 2:
                    d_arrow = trend_emoji_aux.get(d.get("trend", "flat"), "➡️")
                    atv += f"{label}: {d_arrow} {d.get('pct_change', 0):+.2f}%\n"
                else:
                    atv += f"{label}: `N/A`\n"
            atv += f"*±{aux_vt}% threshold*"
            fields.append({"name": f"{aux_emoji} {aux_coin} Trends", "value": atv, "inline": True})

    # === POOL/FLEET SECTION (unified with create_report_embed) ===
    fleet_trend_arrow = "➡️"
    if trends.get("fleet", 0) > 2: fleet_trend_arrow = "📈"
    elif trends.get("fleet", 0) < -2: fleet_trend_arrow = "📉"

    # Unified formatting with create_report_embed - use code block and helper functions
    fleet_hashrate_str = format_hashrate_ths(fleet_ths, symbol=active_coin)
    etb_display = f"~{odds.get('days_per_block', 0):.1f} days" if odds.get('days_per_block', 0) != float('inf') and odds.get('days_per_block', 0) < 99999 else "N/A"
    pv = f"```yaml\nHashrate: {fleet_hashrate_str}\nShare:    {odds.get('share_pct', 0):.4f}%\nDaily:    {odds.get('daily_odds_pct', 0):.1f}%\nETB:      {etb_display}\n```"
    pv += f"{fleet_trend_arrow}"
    blocks_found = data.get("blocks_found", 0)
    if blocks_found and blocks_found > 0:
        pv += f" 🏆 **{blocks_found}** blocks!"
    fields.append({"name": "⛏️ POOL", "value": pv, "inline": True})

    # === REWARD SECTION (unified with create_report_embed) ===
    bri = data.get("bri", {})
    prices = data.get("prices", {})
    earnings = data.get("earnings", {})
    wallet_balance = data.get("wallet_balance")
    coin_emoji = get_coin_emoji(active_coin)

    if bri:
        br = bri.get("sha256_reward", bri.get("scrypt_reward", bri.get("block_reward", DEFAULT_BLOCK_REWARDS.get(active_coin, 0))))
        reward_symbol = bri.get('symbol', active_coin)
        reward_val = f"```fix\n{br:.1f} {reward_symbol}\n```"
        reward_val += f"📦 Block `#{bri.get('block_height', 0):,}`"
        fields.append({"name": f"{coin_emoji} REWARD", "value": reward_val, "inline": True})

    # === WALLET SECTION (separate, unified with create_report_embed) ===
    if wallet_balance is not None and prices:
        wv = f"```diff\n+ {wallet_balance:,.2f} {active_coin}\n```"
        wv += format_wallet_fiat(wallet_balance, prices)
        fields.append({"name": "🏦 WALLET", "value": wv, "inline": True})

    # === RIGS SECTION (unified with create_report_embed) ===
    md = data.get("md", {})
    temps = data.get("temps", {})
    miner_status = data.get("miner_status", {})
    power = data.get("power", {})
    health_scores = data.get("health_scores", {})
    uptime_data = data.get("uptime", {})

    if md:
        rl = []
        total_power = 0
        online_count = 0
        offline_count = 0

        # Sort miners by IP address for consistent ordering
        def sort_key_ip(item):
            name = item[0]
            ip_match = re.search(r'(\d+)\.(\d+)\.(\d+)\.(\d+)', name)
            if ip_match:
                return tuple(int(x) for x in ip_match.groups())
            return (999, 999, 999, name)

        sorted_md = sorted(md.items(), key=sort_key_ip)

        for n, hr in sorted_md:
            is_online = miner_status.get(n) != "offline" and "OFFLINE" not in str(hr)
            if is_online:
                online_count += 1
                status_icon = "🟢"
            else:
                offline_count += 1
                status_icon = "🔴"

            # Build compact rig line (unified format)
            line = f"{status_icon} **{n}**: `{hr}`"

            # Add power consumption
            if power and n in power and power[n] > 0:
                line += f" ⚡`{power[n]:.0f}W`"
                total_power += power[n]

            # Add temperature with thermal indicator emoji
            if temps and n in temps:
                t = temps[n]
                chip_temp = t.get('chip') or t.get('board') or t.get('temp') or 0
                if chip_temp > 0:
                    if chip_temp >= 80:
                        line += f" 🔥`{chip_temp:.0f}°`"
                    elif chip_temp >= 70:
                        line += f" 🌡️`{chip_temp:.0f}°`"
                    else:
                        line += f" ❄️`{chip_temp:.0f}°`"

            # Add uptime badge
            if uptime_data and n in uptime_data:
                uptime_pct = uptime_data[n]
                if uptime_pct >= 99.9:
                    line += " 🏆"
                elif uptime_pct >= 99:
                    line += " ⭐"
                elif uptime_pct < 90:
                    line += " ⚠️"

            rl.append(line)

        # Fleet summary line
        fleet_summary = []
        if total_power > 0:
            fleet_summary.append(f"⚡ **{total_power:.0f}W**")
        if uptime_data:
            avg_uptime = sum(uptime_data.values()) / len(uptime_data) if uptime_data else 100
            fleet_summary.append(f"📊 **{avg_uptime:.1f}%** uptime")
        if fleet_summary:
            rl.append("─" * 20)
            rl.append(" • ".join(fleet_summary))

        # Rig status header (unified format)
        rig_header = f"🟢 {online_count}" + (f" 🔴 {offline_count}" if offline_count > 0 else "")
        fields.append({"name": f"🖥️ RIGS ({rig_header})", "value": "\n".join(rl), "inline": False})

    # === WEEKLY SECTION (if weekly report) ===
    if "weekly" in report_types:
        weekly_stats = data.get("weekly_stats", {})
        uptime = data.get("uptime", {})
        wv = f"📈 Avg Net: {weekly_stats.get('avg_network_phs', 0):.1f} PH/s"
        wv += f"\n📈 Avg Fleet: {weekly_stats.get('avg_fleet_ths', 0):.1f} TH/s"
        wv += f"\n🔴 Offline Events: {weekly_stats.get('offline_events', 0)}"
        fields.append({"name": "📈 WEEKLY SUMMARY", "value": wv, "inline": True})

        if uptime:
            ul = [f"{'🟢' if p>=99 else '🟡' if p>=95 else '🔴'} {n}: {p:.1f}%" for n, p in sorted(uptime.items())]
            fields.append({"name": "⏱️ UPTIME", "value": "\n".join(ul), "inline": True})

    # === MONTHLY SECTION (if monthly report) ===
    if "monthly" in report_types and earnings and active_coin:
        # Use coin-specific key (dgb, btc, bch) - no fallback
        coin_amount = earnings.get(active_coin.lower(), 0)
        coin_precision = 8 if active_coin in ("BTC", "BCH", "BC2", "NMC", "SYS") else 2
        mv = f"{coin_emoji} **{coin_amount:.{coin_precision}f} {active_coin}**"
        cur = get_currency_meta()
        fiat_val = earnings.get(cur["code"], 0)
        mv += f"\n{cur['emoji']} {cur['symbol']}{fiat_val:,.{cur['decimals']}f} {REPORT_CURRENCY}"
        if earnings.get('blocks'): mv += f"\n🏆 **{earnings['blocks']}** blocks"
        fields.append({"name": f"📅 {now.strftime('%B')} Earnings", "value": mv, "inline": True})

    # === QUARTERLY SECTION (if quarterly report) ===
    if "quarterly" in report_types and active_coin:
        quarterly_stats = data.get("quarterly_stats", {})
        # Use coin-specific key - no fallback
        total_coins = quarterly_stats.get(f'total_{active_coin.lower()}', 0)
        coin_precision = 8 if active_coin in ("BTC", "BCH", "BC2", "NMC", "SYS") else 2
        qv = f"🏆 Blocks: **{quarterly_stats.get('total_blocks', 0)}**"
        qv += f"\n{coin_emoji} Mined: **{total_coins:,.{coin_precision}f} {active_coin}**"
        if prices:
            cur = get_currency_meta()
            qv_fiat = total_coins * prices.get(cur["code"], 0)
            qv += f"\n💵 Value: **{cur['symbol']}{qv_fiat:,.{cur['decimals']}f} {REPORT_CURRENCY}**"
        fields.append({"name": f"📅 {data.get('quarter_name', 'Q?')} Summary", "value": qv, "inline": True})

    # === SPECIAL DATE SECTION (if special date) ===
    if "special" in report_types:
        special_info = data.get("special_info", {})
        theme_key = special_info.get("theme_key", "fallback")
        msg = theme(f"special.{theme_key}")
        if msg.startswith("special."):
            msg = theme("special.fallback")
        fields.append({"name": f"{special_info.get('emoji', '🌟')} MINING WISDOM", "value": msg, "inline": False})

    # === DAILY EARNINGS ESTIMATE ===
    if prices and bri:
        br = bri.get("sha256_reward", bri.get("scrypt_reward", bri.get("block_reward", DEFAULT_BLOCK_REWARDS.get(active_coin, 0))))
        daily_coins = br * (odds.get("daily_odds_pct", 0) / 100)
        cur = get_currency_meta()
        coin_price = prices.get(cur["code"], 0)
        dev = f"{cur['symbol']}{coin_price:.6f}/{active_coin}"
        dev += f"\n📈 {daily_coins:.1f} {active_coin}/day ({cur['symbol']}{daily_coins * coin_price:.{cur['decimals']}f})"
        fields.append({"name": "💵 EARNINGS", "value": dev, "inline": True})

    # === INFRASTRUCTURE HEALTH SECTION (if available) ===
    infra_health = data.get("infra_health")
    if infra_health and infra_health.metrics:
        infra_lines = []
        cb_state = infra_health.get_circuit_breaker_state()
        bp_level = infra_health.get_backpressure_level()
        zmq_health = infra_health.get_zmq_health()

        # Show circuit breaker status (only if not healthy)
        if cb_state > 0:
            infra_lines.append(f"⚡ Circuit: {infra_health.get_circuit_breaker_label()}")

        # Show backpressure status (only if elevated)
        if bp_level > 0:
            infra_lines.append(f"📊 Backpressure: {infra_health.get_backpressure_label()}")

        # Show ZMQ status (only if degraded)
        if zmq_health > 2:
            infra_lines.append(f"🔔 ZMQ: {infra_health.get_zmq_health_label()}")

        # Show blocks found/orphaned
        # Use blocks_found from data (lifetime from DB) — Prometheus counter resets on pool restart
        orphans = infra_health.get_blocks_orphaned()
        if blocks_found > 0 or orphans > 0:
            infra_lines.append(f"🏆 Blocks: `{blocks_found}` found, `{orphans}` orphaned")

        # Show share loss if any
        shares_dropped = infra_health.get_shares_dropped()
        if shares_dropped > 0:
            infra_lines.append(f"⚠️ Shares lost: `{shares_dropped}`")

        # If all healthy, show brief healthy status
        if not infra_lines:
            infra_lines.append(f"🟢 Pipeline: Healthy | ZMQ: {infra_health.get_zmq_health_label()}")
            infra_lines.append(f"👥 Workers: `{infra_health.get_active_workers()}`")

        if infra_lines:
            fields.append({"name": "🔧 INFRASTRUCTURE", "value": "\n".join(infra_lines), "inline": True})

    # Build description
    desc = f"*{now.strftime('%Y-%m-%d %H:%M')}*"

    return _embed(title, desc, color, fields)

def create_trend_report_embed(diff_trends, network_trends, fleet_trends, coin_symbol=None):
    """Create a detailed trend analysis embed.

    Supports multi-coin: all supported coins (DGB, BTC, BCH, BC2, LTC, DOGE, DGB-SCRYPT, PEP, CAT).
    For DGB, this reports on the SHA256 portion of the network.
    """
    fields = []
    active_coin = coin_symbol.upper() if coin_symbol else None
    volatility_threshold = get_coin_volatility_threshold(active_coin)

    def format_trend(t, unit="", scale=1):
        if not t:
            return "No data"
        trend_emoji = {"rising": "📈", "falling": "📉", "flat": "➡️"}
        return f"{trend_emoji.get(t.get('trend', 'flat'), '➡️')} {t.get('current', 0)/scale:.2f}{unit} ({t.get('pct_change', 0):+.2f}%)"

    # Difficulty trends
    dv = ""
    for period in ["6h", "12h", "1d", "3d", "7d"]:
        t = diff_trends.get(period)
        if t:
            dv += f"**{period}**: {format_trend(t, 'G', 1e9)}\n"
    if dv:
        algo_note = f" ({get_coin_algorithm(active_coin).upper()})" if active_coin else ""
        coin_label = active_coin or "Network"
        fields.append({"name": f"🎯 {coin_label} Difficulty{algo_note}", "value": dv, "inline": True})

    # Network trends
    nv = ""
    for period in ["6h", "12h", "1d", "3d", "7d"]:
        t = network_trends.get(period)
        if t:
            nv += f"**{period}**: {format_trend(t, ' PH/s')}\n"
    if nv:
        fields.append({"name": f"🌐 {active_coin} Network", "value": nv, "inline": True})

    # Fleet trends
    fv = ""
    for period in ["6h", "12h", "1d", "3d", "7d"]:
        t = fleet_trends.get(period)
        if t:
            fv += f"**{period}**: {format_trend(t, ' TH/s')}\n"
    if fv:
        fields.append({"name": "⛏️ Fleet Hashrate", "value": fv, "inline": True})

    # Add volatility threshold info
    vol_note = f"*Trend threshold: ±{volatility_threshold}% ({get_coin_volatility_description(active_coin).split('(')[0].strip()})*"

    return _embed(theme("trend.title", coin=active_coin), f"*{local_now().strftime('%Y-%m-%d %H:%M')}*\n{vol_note}", COLORS["blue"], fields)

def create_difficulty_report_embed(diff_trends, coin_symbol=None, network_phs=None, odds=None):
    """Create a dedicated difficulty trend report.

    For DGB, this focuses on the SHA256 algorithm portion of the multi-algo chain.
    For BTC/BCH, this shows the full network difficulty trends.

    Includes:
    - Current difficulty with trend arrow
    - 6h, 1d, 3d, 7d, 30d period comparisons
    - Min/max values in each period
    - Impact on mining odds
    """
    fields = []
    active_coin = coin_symbol.upper() if coin_symbol else None
    coin_emoji = get_coin_emoji(active_coin)
    volatility_threshold = get_coin_volatility_threshold(active_coin)

    def format_diff(t, include_minmax=False):
        if not t:
            return "No data"
        trend_emoji = {"rising": "📈", "falling": "📉", "flat": "➡️"}
        result = f"{trend_emoji.get(t.get('trend', 'flat'), '➡️')} **{t.get('current', 0)/1e9:.2f}G** ({t.get('pct_change', 0):+.2f}%)"
        if include_minmax and t.get('min') and t.get('max'):
            result += f"\n   ↓{t['min']/1e9:.2f}G → ↑{t['max']/1e9:.2f}G"
        return result

    # Current difficulty section
    current = diff_trends.get("1d", {}).get("current", 0)
    if current:
        cv = f"{coin_emoji} **{current/1e9:.2f} G**"
        if network_phs:
            cv += f"\n🌐 Network: {network_phs:.2f} PH/s"
        if odds:
            cv += f"\n🎰 Your odds: {odds.get('daily_odds_pct', 0):.2f}%/day"
        fields.append({"name": f"🎯 Current {active_coin} Difficulty", "value": cv, "inline": False})

    # Short-term trends (6h, 1d)
    stv = ""
    for period in ["6h", "12h", "1d"]:
        t = diff_trends.get(period)
        if t:
            stv += f"**{period}**: {format_diff(t)}\n"
    if stv:
        fields.append({"name": "📊 Short-Term", "value": stv, "inline": True})

    # Medium-term trends (3d, 7d)
    mtv = ""
    for period in ["3d", "7d"]:
        t = diff_trends.get(period)
        if t:
            mtv += f"**{period}**: {format_diff(t)}\n"
    if mtv:
        fields.append({"name": "📈 Medium-Term", "value": mtv, "inline": True})

    # Long-term trend (30d)
    lt = diff_trends.get("30d")
    if lt:
        ltv = format_diff(lt, include_minmax=True)
        fields.append({"name": "📅 30-Day", "value": ltv, "inline": True})

    # Mining impact analysis
    if odds and network_phs:
        # Calculate how difficulty changes affect odds
        impact = ""
        t_1d = diff_trends.get("1d", {})
        if t_1d.get("pct_change"):
            pct = t_1d["pct_change"]
            if pct > 0:
                impact = f"⚠️ Difficulty up {pct:.1f}% → harder to find blocks"
            elif pct < 0:
                impact = f"✅ Difficulty down {abs(pct):.1f}% → easier to find blocks"
            else:
                impact = "➡️ Difficulty stable"
        if impact:
            dpb_val = odds.get('days_per_block', 0)
            dpb_str = f"{dpb_val:.1f}" if dpb_val != float('inf') and dpb_val < 99999 else "N/A"
            impact += f"\n📅 Est. days/block: {dpb_str}"
            fields.append({"name": "⚡ Mining Impact", "value": impact, "inline": False})

    # Title includes algo note for DGB
    algo_note = " (SHA256 Algo)" if active_coin == "DGB" else ""
    title = theme("difficulty.title", coin=active_coin, algo=algo_note)
    desc = f"*{local_now().strftime('%Y-%m-%d %H:%M')}*\n*Trend threshold: ±{volatility_threshold}%*"

    return _embed(title, desc, COLORS["cyan"], fields)

# === ACHIEVEMENTS SYSTEM ===
ACHIEVEMENTS = {
    # === BLOCK MILESTONES (20) ===
    "first_blood": {"name": "First Blood", "emoji": "🩸", "desc": "Found your first block", "condition": lambda s: s.get("lifetime_blocks", 0) >= 1},
    "double_tap": {"name": "Double Tap", "emoji": "✌️", "desc": "Found 2 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 2},
    "hat_trick": {"name": "Hat Trick", "emoji": "🎩", "desc": "Found 3 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 3},
    "lucky_seven": {"name": "Lucky Seven", "emoji": "🍀", "desc": "Found 7 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 7},
    "perfect_10": {"name": "Perfect 10", "emoji": "🔟", "desc": "Found 10 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 10},
    "bakers_dozen": {"name": "Baker's Dozen", "emoji": "🥖", "desc": "Found 13 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 13},
    "sweet_16": {"name": "Sweet 16", "emoji": "🎂", "desc": "Found 16 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 16},
    "blackjack": {"name": "Blackjack", "emoji": "🃏", "desc": "Found 21 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 21},
    "quarter_century": {"name": "Quarter Century", "emoji": "🏆", "desc": "Found 25 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 25},
    "half_century": {"name": "Half Century", "emoji": "🎖️", "desc": "Found 50 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 50},
    "centurion": {"name": "Centurion", "emoji": "💯", "desc": "Found 100 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 100},
    "block_hunter": {"name": "Block Hunter", "emoji": "🎯", "desc": "Found 150 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 150},
    "block_slayer": {"name": "Block Slayer", "emoji": "⚔️", "desc": "Found 200 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 200},
    "block_master": {"name": "Block Master", "emoji": "👑", "desc": "Found 250 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 250},
    "block_legend": {"name": "Block Legend", "emoji": "🌟", "desc": "Found 500 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 500},
    "block_god": {"name": "Block God", "emoji": "⚡", "desc": "Found 1000 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 1000},
    "diamond_miner": {"name": "Diamond Miner", "emoji": "💎", "desc": "Found 2500 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 2500},
    "spiral_sage": {"name": "Spiral Sage", "emoji": "🌀", "desc": "Found 5000 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 5000},
    "eternal_hasher": {"name": "Eternal Hasher", "emoji": "♾️", "desc": "Found 10000 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 10000},
    "satoshi_heir": {"name": "Satoshi's Heir", "emoji": "👤", "desc": "Found 21000 blocks", "condition": lambda s: s.get("lifetime_blocks", 0) >= 21000},

    # === DGB EARNINGS MILESTONES (15) ===
    "pocket_change": {"name": "Pocket Change", "emoji": "🪙", "desc": "Earned 100 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 100},
    "small_stack": {"name": "Small Stack", "emoji": "📚", "desc": "Earned 500 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 500},
    "growing_fortune": {"name": "Growing Fortune", "emoji": "🌱", "desc": "Earned 1,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 1000},
    "dgb_thousandaire": {"name": "DGB Thousandaire", "emoji": "💰", "desc": "Earned 5,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 5000},
    "stacking_sats": {"name": "Stacking Sats", "emoji": "📈", "desc": "Earned 10,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 10000},
    "dgb_banker": {"name": "DGB Banker", "emoji": "🏦", "desc": "Earned 25,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 25000},
    "crypto_whale": {"name": "Crypto Whale", "emoji": "🐋", "desc": "Earned 50,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 50000},
    "dgb_millionaire": {"name": "DGB Millionaire", "emoji": "💎", "desc": "Earned 100,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 100000},
    "moon_stacker": {"name": "Moon Stacker", "emoji": "🌙", "desc": "Earned 250,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 250000},
    "half_million_club": {"name": "Half Million Club", "emoji": "🎊", "desc": "Earned 500,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 500000},
    "dgb_mogul": {"name": "DGB Mogul", "emoji": "🏰", "desc": "Earned 1,000,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 1000000},
    "diamond_hands": {"name": "Diamond Hands", "emoji": "💎🙌", "desc": "Earned 2,500,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 2500000},
    "crypto_baron": {"name": "Crypto Baron", "emoji": "👑", "desc": "Earned 5,000,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 5000000},
    "blockchain_billionaire": {"name": "Blockchain Billionaire", "emoji": "🌟", "desc": "Earned 10,000,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 10000000},
    "dgb_god": {"name": "DGB God", "emoji": "⚡", "desc": "Earned 21,000,000 DGB", "condition": lambda s: s.get("lifetime_dgb", 0) >= 21000000},

    # === BTC EARNINGS MILESTONES (10) ===
    "btc_first_sats": {"name": "First Sats", "emoji": "🟠", "desc": "Earned 0.01 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 0.01},
    "btc_stacker": {"name": "BTC Stacker", "emoji": "🟠📚", "desc": "Earned 0.1 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 0.1},
    "btc_hodler": {"name": "BTC Hodler", "emoji": "🟠💎", "desc": "Earned 0.5 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 0.5},
    "btc_whole_coiner": {"name": "Whole Coiner", "emoji": "🟠🪙", "desc": "Earned 1 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 1},
    "btc_double_stack": {"name": "Double Stack", "emoji": "🟠🟠", "desc": "Earned 2 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 2},
    "btc_high_five": {"name": "BTC High Five", "emoji": "🟠🖐️", "desc": "Earned 5 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 5},
    "btc_ten_stack": {"name": "BTC Decacoiner", "emoji": "🟠🔟", "desc": "Earned 10 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 10},
    "btc_whale": {"name": "BTC Whale", "emoji": "🟠🐋", "desc": "Earned 21 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 21},
    "btc_mega_whale": {"name": "BTC Mega Whale", "emoji": "🟠👑", "desc": "Earned 50 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 50},
    "btc_legend": {"name": "BTC Legend", "emoji": "🟠⚡", "desc": "Earned 100 BTC", "condition": lambda s: s.get("lifetime_btc", 0) >= 100},

    # === BCH EARNINGS MILESTONES (10) ===
    "bch_first_cash": {"name": "First Cash", "emoji": "🟢", "desc": "Earned 0.01 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 0.01},
    "bch_stacker": {"name": "BCH Stacker", "emoji": "🟢📚", "desc": "Earned 0.1 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 0.1},
    "bch_holder": {"name": "BCH Holder", "emoji": "🟢💎", "desc": "Earned 0.5 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 0.5},
    "bch_whole_coiner": {"name": "BCH Whole Coiner", "emoji": "🟢🪙", "desc": "Earned 1 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 1},
    "bch_double_stack": {"name": "BCH Double Stack", "emoji": "🟢🟢", "desc": "Earned 2 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 2},
    "bch_high_five": {"name": "BCH High Five", "emoji": "🟢🖐️", "desc": "Earned 5 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 5},
    "bch_ten_stack": {"name": "BCH Decacoiner", "emoji": "🟢🔟", "desc": "Earned 10 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 10},
    "bch_whale": {"name": "BCH Whale", "emoji": "🟢🐋", "desc": "Earned 21 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 21},
    "bch_mega_whale": {"name": "BCH Mega Whale", "emoji": "🟢👑", "desc": "Earned 50 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 50},
    "bch_legend": {"name": "BCH Legend", "emoji": "🟢⚡", "desc": "Earned 100 BCH", "condition": lambda s: s.get("lifetime_bch", 0) >= 100},

    # === BC2 EARNINGS MILESTONES (10) ===
    "bc2_first_coins": {"name": "BC2 First Coins", "emoji": "🔵", "desc": "Earned 0.01 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 0.01},
    "bc2_stacker": {"name": "BC2 Stacker", "emoji": "🔵📚", "desc": "Earned 0.1 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 0.1},
    "bc2_holder": {"name": "BC2 Holder", "emoji": "🔵💎", "desc": "Earned 0.5 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 0.5},
    "bc2_whole_coiner": {"name": "BC2 Whole Coiner", "emoji": "🔵🪙", "desc": "Earned 1 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 1},
    "bc2_double_stack": {"name": "BC2 Double Stack", "emoji": "🔵🔵", "desc": "Earned 2 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 2},
    "bc2_high_five": {"name": "BC2 High Five", "emoji": "🔵🖐️", "desc": "Earned 5 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 5},
    "bc2_ten_stack": {"name": "BC2 Decacoiner", "emoji": "🔵🔟", "desc": "Earned 10 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 10},
    "bc2_whale": {"name": "BC2 Whale", "emoji": "🔵🐋", "desc": "Earned 21 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 21},
    "bc2_mega_whale": {"name": "BC2 Mega Whale", "emoji": "🔵👑", "desc": "Earned 50 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 50},
    "bc2_legend": {"name": "BC2 Legend", "emoji": "🔵⚡", "desc": "Earned 100 BC2", "condition": lambda s: s.get("lifetime_bc2", 0) >= 100},

    # === LTC EARNINGS MILESTONES (10) ===
    "ltc_first_lites": {"name": "First Lites", "emoji": "⚪", "desc": "Earned 0.01 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 0.01},
    "ltc_stacker": {"name": "LTC Stacker", "emoji": "⚪📚", "desc": "Earned 0.1 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 0.1},
    "ltc_holder": {"name": "LTC Holder", "emoji": "⚪💎", "desc": "Earned 0.5 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 0.5},
    "ltc_whole_coiner": {"name": "LTC Whole Coiner", "emoji": "⚪🪙", "desc": "Earned 1 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 1},
    "ltc_double_stack": {"name": "LTC Double Stack", "emoji": "⚪⚪", "desc": "Earned 2 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 2},
    "ltc_high_five": {"name": "LTC High Five", "emoji": "⚪🖐️", "desc": "Earned 5 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 5},
    "ltc_ten_stack": {"name": "LTC Decacoiner", "emoji": "⚪🔟", "desc": "Earned 10 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 10},
    "ltc_whale": {"name": "LTC Whale", "emoji": "⚪🐋", "desc": "Earned 21 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 21},
    "ltc_mega_whale": {"name": "LTC Mega Whale", "emoji": "⚪👑", "desc": "Earned 50 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 50},
    "ltc_legend": {"name": "LTC Legend", "emoji": "⚪⚡", "desc": "Earned 100 LTC", "condition": lambda s: s.get("lifetime_ltc", 0) >= 100},

    # === DOGE EARNINGS MILESTONES (10) ===
    "doge_first_doges": {"name": "First Doges", "emoji": "🐕", "desc": "Earned 100 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 100},
    "doge_stacker": {"name": "DOGE Stacker", "emoji": "🐕📚", "desc": "Earned 1,000 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 1000},
    "doge_holder": {"name": "DOGE Holder", "emoji": "🐕💎", "desc": "Earned 5,000 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 5000},
    "doge_thousandaire": {"name": "DOGE Thousandaire", "emoji": "🐕🪙", "desc": "Earned 10,000 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 10000},
    "doge_moon": {"name": "To The Moon", "emoji": "🐕🌙", "desc": "Earned 25,000 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 25000},
    "doge_high_five": {"name": "DOGE High Five", "emoji": "🐕🖐️", "desc": "Earned 50,000 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 50000},
    "doge_whale": {"name": "DOGE Whale", "emoji": "🐕🐋", "desc": "Earned 100,000 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 100000},
    "doge_millionaire": {"name": "DOGE Millionaire", "emoji": "🐕💰", "desc": "Earned 1,000,000 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 1000000},
    "doge_mega_whale": {"name": "DOGE Mega Whale", "emoji": "🐕👑", "desc": "Earned 10,000,000 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 10000000},
    "doge_legend": {"name": "DOGE Legend", "emoji": "🐕⚡", "desc": "Earned 100,000,000 DOGE", "condition": lambda s: s.get("lifetime_doge", 0) >= 100000000},

    # === UPTIME ACHIEVEMENTS (15) ===
    "always_on": {"name": "Always On", "emoji": "🔌", "desc": "24 hours continuous uptime", "condition": lambda s: s.get("max_uptime_hours", 0) >= 24},
    "weekend_warrior": {"name": "Weekend Warrior", "emoji": "🗓️", "desc": "48 hours continuous uptime", "condition": lambda s: s.get("max_uptime_hours", 0) >= 48},
    "week_streak": {"name": "Week Streak", "emoji": "📅", "desc": "7 days continuous uptime", "condition": lambda s: s.get("max_uptime_hours", 0) >= 168},
    "fortnight_fighter": {"name": "Fortnight Fighter", "emoji": "⚔️", "desc": "14 days continuous uptime", "condition": lambda s: s.get("max_uptime_hours", 0) >= 336},
    "monthly_machine": {"name": "Monthly Machine", "emoji": "🤖", "desc": "30 days continuous uptime", "condition": lambda s: s.get("max_uptime_hours", 0) >= 720},
    "quarterly_champion": {"name": "Quarterly Champion", "emoji": "🏅", "desc": "90 days continuous uptime", "condition": lambda s: s.get("max_uptime_hours", 0) >= 2160},
    "half_year_hero": {"name": "Half Year Hero", "emoji": "🦸", "desc": "180 days continuous uptime", "condition": lambda s: s.get("max_uptime_hours", 0) >= 4320},
    "annual_ace": {"name": "Annual Ace", "emoji": "🎖️", "desc": "365 days continuous uptime", "condition": lambda s: s.get("max_uptime_hours", 0) >= 8760},
    "iron_will": {"name": "Iron Will", "emoji": "🔩", "desc": "99% uptime over 30 days", "condition": lambda s: s.get("monthly_uptime_pct", 0) >= 99},
    "titanium_resolve": {"name": "Titanium Resolve", "emoji": "⚙️", "desc": "99.5% uptime over 30 days", "condition": lambda s: s.get("monthly_uptime_pct", 0) >= 99.5},
    "diamond_reliability": {"name": "Diamond Reliability", "emoji": "💎", "desc": "99.9% uptime over 30 days", "condition": lambda s: s.get("monthly_uptime_pct", 0) >= 99.9},
    "perfect_attendance": {"name": "Perfect Attendance", "emoji": "✅", "desc": "100% uptime for a full week", "condition": lambda s: s.get("perfect_week", False)},
    "no_days_off": {"name": "No Days Off", "emoji": "💪", "desc": "100% uptime for a full month", "condition": lambda s: s.get("perfect_month", False)},
    "unstoppable": {"name": "Unstoppable", "emoji": "🚀", "desc": "100% uptime for 3 months", "condition": lambda s: s.get("perfect_quarter", False)},
    "eternal_flame": {"name": "Eternal Flame", "emoji": "🔥", "desc": "100% uptime for 1 year", "condition": lambda s: s.get("perfect_year", False)},

    # === HASHRATE ACHIEVEMENTS (15) ===
    "getting_started": {"name": "Getting Started", "emoji": "🐣", "desc": "Reached 1 TH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 1},
    "warming_up": {"name": "Warming Up", "emoji": "🔥", "desc": "Reached 5 TH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 5},
    "terahash_titan": {"name": "Terahash Titan", "emoji": "⚡", "desc": "Reached 10 TH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 10},
    "hash_force": {"name": "Hash Force", "emoji": "💨", "desc": "Reached 25 TH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 25},
    "power_player": {"name": "Power Player", "emoji": "🎮", "desc": "Reached 50 TH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 50},
    "hash_hurricane": {"name": "Hash Hurricane", "emoji": "🌀", "desc": "Reached 100 TH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 100},
    "petahash_pioneer": {"name": "Petahash Pioneer", "emoji": "🚀", "desc": "Reached 250 TH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 250},
    "hash_overlord": {"name": "Hash Overlord", "emoji": "👑", "desc": "Reached 500 TH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 500},
    "petahash_player": {"name": "Petahash Player", "emoji": "🌟", "desc": "Reached 1 PH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 1000},
    "hash_emperor": {"name": "Hash Emperor", "emoji": "🏛️", "desc": "Reached 2 PH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 2000},
    "network_force": {"name": "Network Force", "emoji": "🌐", "desc": "Reached 5 PH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 5000},
    "hash_god": {"name": "Hash God", "emoji": "⚡", "desc": "Reached 10 PH/s fleet hashrate", "condition": lambda s: s.get("max_fleet_ths", 0) >= 10000},
    "efficiency_expert": {"name": "Efficiency Expert", "emoji": "📊", "desc": "Maintained 50+ J/TH efficiency", "condition": lambda s: s.get("best_efficiency", 999) <= 50},
    "green_miner": {"name": "Green Miner", "emoji": "🌿", "desc": "Maintained 40+ J/TH efficiency", "condition": lambda s: s.get("best_efficiency", 999) <= 40},
    "ultra_efficient": {"name": "Ultra Efficient", "emoji": "⚡", "desc": "Maintained 30+ J/TH efficiency", "condition": lambda s: s.get("best_efficiency", 999) <= 30},

    # === FLEET MANAGEMENT (15) ===
    "solo_warrior": {"name": "Solo Warrior", "emoji": "🗡️", "desc": "Mining with 1 device", "condition": lambda s: s.get("miner_count", 0) >= 1},
    "dynamic_duo": {"name": "Dynamic Duo", "emoji": "👯", "desc": "Mining with 2 devices", "condition": lambda s: s.get("miner_count", 0) >= 2},
    "three_musketeers": {"name": "Three Musketeers", "emoji": "⚔️", "desc": "Mining with 3 devices", "condition": lambda s: s.get("miner_count", 0) >= 3},
    "fantastic_four": {"name": "Fantastic Four", "emoji": "4️⃣", "desc": "Mining with 4 devices", "condition": lambda s: s.get("miner_count", 0) >= 4},
    "high_five": {"name": "High Five", "emoji": "🖐️", "desc": "Mining with 5 devices", "condition": lambda s: s.get("miner_count", 0) >= 5},
    "lucky_seven_fleet": {"name": "Lucky Seven Fleet", "emoji": "🎰", "desc": "Mining with 7 devices", "condition": lambda s: s.get("miner_count", 0) >= 7},
    "fleet_commander": {"name": "Fleet Commander", "emoji": "🚢", "desc": "Mining with 10 devices", "condition": lambda s: s.get("miner_count", 0) >= 10},
    "armada_admiral": {"name": "Armada Admiral", "emoji": "⚓", "desc": "Mining with 15 devices", "condition": lambda s: s.get("miner_count", 0) >= 15},
    "mining_mogul": {"name": "Mining Mogul", "emoji": "🏭", "desc": "Mining with 20 devices", "condition": lambda s: s.get("miner_count", 0) >= 20},
    "hash_factory": {"name": "Hash Factory", "emoji": "🏗️", "desc": "Mining with 30 devices", "condition": lambda s: s.get("miner_count", 0) >= 30},
    "industrial_scale": {"name": "Industrial Scale", "emoji": "⚙️", "desc": "Mining with 50 devices", "condition": lambda s: s.get("miner_count", 0) >= 50},
    "mega_farm": {"name": "Mega Farm", "emoji": "🌾", "desc": "Mining with 100 devices", "condition": lambda s: s.get("miner_count", 0) >= 100},
    "diverse_fleet": {"name": "Diverse Fleet", "emoji": "🌈", "desc": "Using 3+ different miner types", "condition": lambda s: s.get("miner_types", 0) >= 3},
    "collector": {"name": "Collector", "emoji": "🎯", "desc": "Using 5+ different miner types", "condition": lambda s: s.get("miner_types", 0) >= 5},
    "fleet_harmony": {"name": "Fleet Harmony", "emoji": "🎵", "desc": "All miners online simultaneously for 24h", "condition": lambda s: s.get("all_online_24h", False)},

    # === TEMPERATURE MASTERY (10) ===
    "cool_operator": {"name": "Cool Operator", "emoji": "❄️", "desc": "All miners under 50°C", "condition": lambda s: s.get("max_temp", 999) < 50},
    "thermal_master": {"name": "Thermal Master", "emoji": "🌡️", "desc": "All miners under 60°C for 24h", "condition": lambda s: s.get("cool_24h", False)},
    "ice_cold": {"name": "Ice Cold", "emoji": "🧊", "desc": "Maintained under 45°C average", "condition": lambda s: s.get("avg_temp", 999) < 45},
    "heat_tamer": {"name": "Heat Tamer", "emoji": "🔥", "desc": "Recovered from critical temp event", "condition": lambda s: s.get("temp_recoveries", 0) >= 1},
    "thermal_warrior": {"name": "Thermal Warrior", "emoji": "⚔️", "desc": "Survived 10 temp warnings", "condition": lambda s: s.get("temp_warnings", 0) >= 10},
    "no_throttle": {"name": "No Throttle", "emoji": "🚫", "desc": "No thermal throttling for 7 days", "condition": lambda s: s.get("throttle_free_days", 0) >= 7},
    "arctic_ops": {"name": "Arctic Ops", "emoji": "🏔️", "desc": "Maintained under 40°C for 24h", "condition": lambda s: s.get("arctic_24h", False)},
    "summer_survivor": {"name": "Summer Survivor", "emoji": "☀️", "desc": "No critical temps during summer", "condition": lambda s: s.get("summer_survived", False)},
    "thermal_zen": {"name": "Thermal Zen", "emoji": "🧘", "desc": "Less than 5°C temp variance for 24h", "condition": lambda s: s.get("temp_stable_24h", False)},
    "overclocker": {"name": "Overclocker", "emoji": "⚡", "desc": "Ran stable at 80°C+ for 1 hour", "condition": lambda s: s.get("hot_stable_1h", False)},

    # === TIMING & LUCK (15) ===
    "midnight_miner": {"name": "Midnight Miner", "emoji": "🌙", "desc": "Found a block between midnight-3am", "condition": lambda s: s.get("midnight_block", False)},
    "early_bird": {"name": "Early Bird", "emoji": "🐦", "desc": "Found a block between 5-7am", "condition": lambda s: s.get("early_block", False)},
    "lunch_break": {"name": "Lunch Break Lucky", "emoji": "🥪", "desc": "Found a block at noon", "condition": lambda s: s.get("noon_block", False)},
    "happy_hour": {"name": "Happy Hour", "emoji": "🍺", "desc": "Found a block at 5pm", "condition": lambda s: s.get("happy_hour_block", False)},
    "witching_hour": {"name": "Witching Hour", "emoji": "🧙", "desc": "Found a block at exactly 3:33am", "condition": lambda s: s.get("witching_block", False)},
    "new_year_block": {"name": "New Year Block", "emoji": "🎆", "desc": "Found a block on January 1st", "condition": lambda s: s.get("newyear_block", False)},
    "birthday_luck": {"name": "Birthday Luck", "emoji": "🎂", "desc": "Found a block on your birthday", "condition": lambda s: s.get("birthday_block", False)},
    "friday_13th": {"name": "Friday 13th", "emoji": "🖤", "desc": "Found a block on Friday the 13th", "condition": lambda s: s.get("friday13_block", False)},
    "halloween_haul": {"name": "Halloween Haul", "emoji": "🎃", "desc": "Found a block on Halloween", "condition": lambda s: s.get("halloween_block", False)},
    "christmas_miracle": {"name": "Christmas Miracle", "emoji": "🎄", "desc": "Found a block on Christmas", "condition": lambda s: s.get("christmas_block", False)},
    "lucky_streak": {"name": "Lucky Streak", "emoji": "🔥", "desc": "Found 2 blocks in same day", "condition": lambda s: s.get("double_day", False)},
    "triple_threat": {"name": "Triple Threat", "emoji": "🎯", "desc": "Found 3 blocks in same day", "condition": lambda s: s.get("triple_day", False)},
    "lightning_luck": {"name": "Lightning Luck", "emoji": "⚡", "desc": "Found 2 blocks within 1 hour", "condition": lambda s: s.get("blocks_1h", 0) >= 2},
    "against_odds": {"name": "Against the Odds", "emoji": "🎲", "desc": "Found block with <10% daily odds", "condition": lambda s: s.get("low_odds_block", False)},
    "impossible_block": {"name": "Impossible Block", "emoji": "🌟", "desc": "Found block with <1% daily odds", "condition": lambda s: s.get("impossible_block", False)},

    # === RESILIENCE & RECOVERY (15) ===
    "comeback_kid": {"name": "Comeback Kid", "emoji": "🔄", "desc": "Recovered from offline status", "condition": lambda s: s.get("recoveries", 0) >= 1},
    "resilient_miner": {"name": "Resilient Miner", "emoji": "💪", "desc": "Recovered 10 times", "condition": lambda s: s.get("recoveries", 0) >= 10},
    "phoenix_rising": {"name": "Phoenix Rising", "emoji": "🔥", "desc": "Recovered 50 times", "condition": lambda s: s.get("recoveries", 0) >= 50},
    "immortal": {"name": "Immortal", "emoji": "♾️", "desc": "Recovered 100 times", "condition": lambda s: s.get("recoveries", 0) >= 100},
    "power_surge_survivor": {"name": "Power Surge Survivor", "emoji": "⚡", "desc": "Recovered from power blip", "condition": lambda s: s.get("blip_recoveries", 0) >= 1},
    "storm_rider": {"name": "Storm Rider", "emoji": "🌩️", "desc": "Survived 10 power events", "condition": lambda s: s.get("blip_recoveries", 0) >= 10},
    "grid_warrior": {"name": "Grid Warrior", "emoji": "🔌", "desc": "Survived 50 power events", "condition": lambda s: s.get("blip_recoveries", 0) >= 50},
    "zombie_slayer": {"name": "Zombie Slayer", "emoji": "🧟", "desc": "Auto-restarted a zombie miner", "condition": lambda s: s.get("zombie_kills", 0) >= 1},
    "zombie_hunter": {"name": "Zombie Hunter", "emoji": "🏹", "desc": "Auto-restarted 10 zombie miners", "condition": lambda s: s.get("zombie_kills", 0) >= 10},
    "necromancer": {"name": "Necromancer", "emoji": "💀", "desc": "Auto-restarted 50 zombie miners", "condition": lambda s: s.get("zombie_kills", 0) >= 50},
    "self_healer": {"name": "Self Healer", "emoji": "💚", "desc": "Auto-restart successful", "condition": lambda s: s.get("auto_restarts", 0) >= 1},
    "auto_doctor": {"name": "Auto Doctor", "emoji": "🏥", "desc": "25 successful auto-restarts", "condition": lambda s: s.get("auto_restarts", 0) >= 25},
    "maintenance_master": {"name": "Maintenance Master", "emoji": "🔧", "desc": "100 successful auto-restarts", "condition": lambda s: s.get("auto_restarts", 0) >= 100},
    "quick_recovery": {"name": "Quick Recovery", "emoji": "⚡", "desc": "Recovered in under 5 minutes", "condition": lambda s: s.get("quick_recoveries", 0) >= 1},
    "flash_restart": {"name": "Flash Restart", "emoji": "💨", "desc": "Recovered in under 1 minute", "condition": lambda s: s.get("flash_recoveries", 0) >= 1},

    # === NETWORK TIMING (10) ===
    "low_tide": {"name": "Low Tide", "emoji": "🌊", "desc": "Mined during <40 PH/s network", "condition": lambda s: s.get("low_network_block", False)},
    "golden_hour": {"name": "Golden Hour", "emoji": "🌅", "desc": "Found block during network dip", "condition": lambda s: s.get("dip_block", False)},
    "patient_predator": {"name": "Patient Predator", "emoji": "🐆", "desc": "Waited 5+ days for a block", "condition": lambda s: s.get("patient_block", False)},
    "instant_gratification": {"name": "Instant Gratification", "emoji": "⚡", "desc": "Found block within 24h of starting", "condition": lambda s: s.get("fast_first_block", False)},
    "network_watcher": {"name": "Network Watcher", "emoji": "👁️", "desc": "Observed 1000 network changes", "condition": lambda s: s.get("network_observations", 0) >= 1000},
    "drop_hunter": {"name": "Drop Hunter", "emoji": "📉", "desc": "Caught 10 network drops", "condition": lambda s: s.get("drops_caught", 0) >= 10},
    "difficulty_dancer": {"name": "Difficulty Dancer", "emoji": "💃", "desc": "Mined through 100 diff changes", "condition": lambda s: s.get("diff_changes", 0) >= 100},
    "market_timer": {"name": "Market Timer", "emoji": "📈", "desc": "Found block when price >$0.01", "condition": lambda s: s.get("high_price_block", False)},
    "bottom_feeder": {"name": "Bottom Feeder", "emoji": "🐟", "desc": "Found block when price <$0.005", "condition": lambda s: s.get("low_price_block", False)},
    "volatility_victor": {"name": "Volatility Victor", "emoji": "📊", "desc": "Mined through 50% price swing", "condition": lambda s: s.get("volatility_block", False)},

    # === SPECIAL & SECRET (15) ===
    "genesis_miner": {"name": "Genesis Miner", "emoji": "🌟", "desc": "Spiral Sentinel beta tester", "condition": lambda s: s.get("beta_tester", False)},
    "early_adopter": {"name": "Early Adopter", "emoji": "🚀", "desc": "Started mining before 2025", "condition": lambda s: s.get("early_adopter", False)},
    "spiral_supporter": {"name": "Spiral Supporter", "emoji": "🌀", "desc": "Contributed to Spiral Pool", "condition": lambda s: s.get("contributor", False)},
    "bug_hunter": {"name": "Bug Hunter", "emoji": "🐛", "desc": "Reported a valid bug", "condition": lambda s: s.get("bug_reporter", False)},
    "share_whale": {"name": "Share Whale", "emoji": "🐋", "desc": "Submitted 1,000,000 shares", "condition": lambda s: s.get("total_shares", 0) >= 1000000},
    "share_leviathan": {"name": "Share Leviathan", "emoji": "🦑", "desc": "Submitted 10,000,000 shares", "condition": lambda s: s.get("total_shares", 0) >= 10000000},
    "palindrome_block": {"name": "Palindrome Block", "emoji": "🔢", "desc": "Found block with palindrome number", "condition": lambda s: s.get("palindrome_block", False)},
    "repeating_digits": {"name": "Repeating Digits", "emoji": "🔁", "desc": "Found block like #11111111", "condition": lambda s: s.get("repeating_block", False)},
    "round_number": {"name": "Round Number", "emoji": "⭕", "desc": "Found block ending in 000000", "condition": lambda s: s.get("round_block", False)},
    "sequential_block": {"name": "Sequential Block", "emoji": "📊", "desc": "Found block like #12345678", "condition": lambda s: s.get("sequential_block", False)},
    "power_of_two": {"name": "Power of Two", "emoji": "2️⃣", "desc": "Found block that's a power of 2", "condition": lambda s: s.get("pow2_block", False)},
    "fibonacci_finder": {"name": "Fibonacci Finder", "emoji": "🐚", "desc": "Found block in Fibonacci sequence", "condition": lambda s: s.get("fibonacci_block", False)},
    "prime_hunter": {"name": "Prime Hunter", "emoji": "🔢", "desc": "Found block that's a prime number", "condition": lambda s: s.get("prime_block", False)},
    "answer_to_everything": {"name": "Answer to Everything", "emoji": "🌌", "desc": "Found block #42xxxxxx", "condition": lambda s: s.get("42_block", False)},
    "bitcoin_birthday": {"name": "Bitcoin Birthday", "emoji": "🎂", "desc": "Mining on January 3rd", "condition": lambda s: s.get("btc_birthday", False)},

    # === DIGIBYTE SPECIFIC (10) ===
    "dgb_believer": {"name": "DGB Believer", "emoji": "💙", "desc": "Mining DGB for 30+ days", "condition": lambda s: s.get("mining_days", 0) >= 30},
    "dgb_veteran": {"name": "DGB Veteran", "emoji": "🎖️", "desc": "Mining DGB for 180+ days", "condition": lambda s: s.get("mining_days", 0) >= 180},
    "dgb_legend": {"name": "DGB Legend", "emoji": "👑", "desc": "Mining DGB for 365+ days", "condition": lambda s: s.get("mining_days", 0) >= 365},
    "multi_algo_aware": {"name": "Multi-Algo Aware", "emoji": "🔄", "desc": "Understanding DGB's 5 algos", "condition": lambda s: s.get("algo_educated", False)},
    "sha256_specialist": {"name": "SHA256 Specialist", "emoji": "🎯", "desc": "Focused on SHA256d algorithm", "condition": lambda s: s.get("sha256_focused", True)},
    "odocrypt_curious": {"name": "Odocrypt Curious", "emoji": "🔐", "desc": "Learned about Odocrypt algo", "condition": lambda s: s.get("odo_curious", False)},
    "scrypt_scholar": {"name": "Scrypt Scholar", "emoji": "📚", "desc": "Learned about Scrypt algo", "condition": lambda s: s.get("scrypt_scholar", False)},
    "qubit_questioner": {"name": "Qubit Questioner", "emoji": "❓", "desc": "Learned about Qubit algo", "condition": lambda s: s.get("qubit_quest", False)},
    "skein_seeker": {"name": "Skein Seeker", "emoji": "🧶", "desc": "Learned about Skein algo", "condition": lambda s: s.get("skein_seeker", False)},
    "digibyte_advocate": {"name": "DigiByte Advocate", "emoji": "📣", "desc": "Spread the word about DGB", "condition": lambda s: s.get("advocate", False)},
}

def create_achievement_embed(achievement_id, achievement):
    return _embed(
        theme("achievement.title"),
        f"{achievement['emoji']} **{achievement['name']}**\n_{achievement['desc']}_",
        COLORS["gold"],
        footer=theme("achievement.footer", version=__version__)
    )

class AchievementTracker:
    def __init__(self, state):
        self.state = state
        self.unlocked = set(state.lifetime_stats.get("achievements", []))

    def check_achievements(self, stats):
        """Check all achievements and return newly unlocked ones"""
        newly_unlocked = []
        for aid, ach in ACHIEVEMENTS.items():
            if aid not in self.unlocked:
                try:
                    if ach["condition"](stats):
                        self.unlocked.add(aid)
                        newly_unlocked.append((aid, ach))
                except (KeyError, TypeError, ValueError, ZeroDivisionError):
                    pass  # Achievement condition failed to evaluate
        return newly_unlocked

    def save(self):
        self.state.lifetime_stats["achievements"] = list(self.unlocked)

    def get_progress(self):
        return len(self.unlocked), len(ACHIEVEMENTS)

    def get_unlocked_list(self):
        return [(aid, ACHIEVEMENTS[aid]) for aid in self.unlocked if aid in ACHIEVEMENTS]

# === MONITOR STATE ===
class MonitorState:
    _PERSIST_KEYS = ["last_report_hour","last_weekly_report","last_monthly_report","last_quarterly_report","last_special_date","last_maintenance_reminder","last_alerts","miner_offline_since","miner_restart_times","zombie_kick_times","temp_alert_sent","miner_offline_alert_sent","miner_last_uptime","network_history","block_history","miner_health_history","miner_temp_history","miner_hashrate_history","earnings","weekly_stats","quarterly_stats","lifetime_stats","miner_uptimes","miner_block_counts","miner_stale_history","miner_hashrate_baseline","recent_blips","pool_share_history","network_crash_first_detected","network_crash_alert_sent","network_baseline_phs","pool_drop_first_detected","pool_drop_alert_sent","expected_fleet_ths","pool_blocks_found","personal_bests","last_daily_report","hashrate_history_24h","coin_changes","mode_changes","pending_alerts","chronic_issues","miner_pool_hashrate","global_alert_batch","last_batch_flush","miner_stable_online_since","known_block_statuses","orphan_alerts_sent","seen_pool_block_hashes","sats_history","sats_surge_last_alert","high_odds_last_alert","high_odds_first_detected","thermal_critical_since","thermal_shutdown_sent","fan_alert_sent","last_known_orphan_count","zmq_stale_alerted","worker_count_baseline","share_loss_alerted","last_block_notify_mode","last_replica_count","circuit_breaker_alerted","backpressure_alerted","last_wal_write_errors","last_wal_commit_errors","zmq_disconnected_alerted","known_miner_pool_urls","url_mismatch_alerted","hashboard_alert_sent","miner_hw_errors","hw_error_alert_sent","best_share_difficulty","price_history","price_crash_last_alert","last_wallet_balance","wallet_balance_last_check","missing_payout_alerted","payout_deferred_from_quiet","previous_month_earnings","revenue_decline_alerted"]

    def __init__(self):
        self.data_dir = DATA_DIR
        try:
            self.data_dir.mkdir(parents=True, exist_ok=True)
        except (PermissionError, OSError):
            pass  # Directory already handled at module level
        self.state_file = self.data_dir / "state.json"
        self._init_state()
        self.load()

    def _init_state(self):
        self.last_report_hour = None
        self.last_weekly_report = None
        self.last_monthly_report = None
        self.last_quarterly_report = None  # Track last quarter reported
        self.last_special_date = None  # Track last special date (equinox/solstice)
        self.last_maintenance_reminder = None  # Track last monthly maintenance reminder (YYYY-MM format)
        self.last_alerts = {}
        self.miner_offline_since = {}
        self.miner_restart_times = {}
        self.zombie_kick_times = {}   # name → timestamp of last stratum kick attempt
        self.temp_alert_sent = {}
        self.miner_offline_alert_sent = {}  # Separate from temp_alert_sent to avoid suppression
        self.miner_last_uptime = {}
        self.network_history = []
        self.block_history = []
        self.miner_health_history = {}
        self.miner_temp_history = {}
        self.miner_hashrate_history = {}
        # Multi-coin earnings tracking: per-coin and total (all supported coins)
        self.earnings = {"monthly_blocks": 0, "monthly_start": time.time(), "lifetime_blocks": 0}
        for _coin in SUPPORTED_COIN_SYMBOLS:
            self.earnings[f"monthly_{_coin}"] = 0
            self.earnings[f"lifetime_{_coin}"] = 0
        self.weekly_stats = self._new_weekly_stats()
        self.quarterly_stats = self._new_quarterly_stats()
        self.lifetime_stats = {"start_time": time.time()}
        self.miner_uptimes = {}
        self.miner_block_counts = {}
        self.miner_stale_history = {}
        self.miner_hashrate_baseline = {}
        self.recent_blips = []
        self.pool_share_history = {}  # Track pool-side share verification

        # High odds alert tracking — per-coin (1-hour sustained, one alert per episode, respects quiet hours)
        # Keys are coin symbols (e.g., "DGB", "NMC"), values are timestamps
        self.high_odds_last_alert = {}  # {coin: timestamp} of last alert sent per coin
        self.high_odds_first_detected = {}  # {coin: timestamp} when high odds first detected per coin
        self.high_odds_session_alerted = {}  # {coin: bool} True if already alerted this high-odds episode

        # Network hashrate crash tracking (25%+ drop sustained for 30 minutes)
        self.network_crash_first_detected = None
        self.network_crash_alert_sent = False
        self.network_baseline_phs = None  # Rolling baseline for crash detection

        # Pool hashrate drop tracking (50%+ drop, 15min sustained)
        self.pool_drop_first_detected = None
        self.pool_drop_alert_sent = False
        self.expected_fleet_ths = CONFIG.get("expected_fleet_ths", 22.0)  # Expected total fleet hashrate
        self.expected_fleet_ths_disabled = CONFIG.get("expected_fleet_ths_disabled", False)  # True if user skipped hashrate setting

        # Pool block counter — initialised to 0 here; load() will restore from
        # state.json and then re-seed from the database (whichever is higher wins).
        self.pool_blocks_found = 0

        # Personal bests tracking
        self.personal_bests = {
            "highest_fleet_ths": 0,
            "highest_fleet_ths_date": None,
            "fastest_block_seconds": None,  # Time between blocks
            "fastest_block_date": None,
            "longest_uptime_streak_hours": 0,
            "longest_uptime_streak_date": None,
            "highest_daily_odds_pct": 0,
            "highest_daily_odds_date": None,
            "uptime_streak_start": None,  # Persisted streak start time (survives restarts)
        }

        # Daily report tracking (for daily digest mode)
        self.last_daily_report = None

        # 24-hour hashrate history for trend arrows
        self.hashrate_history_24h = []

        # Multi-coin mode tracking
        self.coin_changes = []   # History of coin change events
        self.mode_changes = []   # History of mode switch events (solo <-> multi)

        # ═══════════════════════════════════════════════════════════════════════════════
        # ALERT BATCHING/AGGREGATION - Reduces Discord spam by combining related alerts
        # ═══════════════════════════════════════════════════════════════════════════════
        # Batched alerts are collected within a time window and sent as a single digest.
        # This is especially useful during power events when multiple miners go offline.
        self.pending_alerts = {}  # {miner_name: [{"type": alert_type, "time": timestamp, "embed": embed}, ...]}
        self.alert_context_window = ALERT_BATCH_WINDOW  # Configurable window (default 5 minutes)
        self.global_alert_batch = []  # [{type, embed, miner, time}, ...] for fleet-wide batching
        self.last_batch_flush = time.time()  # Track when we last flushed the batch

        # ═══════════════════════════════════════════════════════════════════════════════
        # M-4: ALERT HYSTERESIS - Prevents flapping alerts
        # ═══════════════════════════════════════════════════════════════════════════════
        # Tracks how long a miner has been in a stable state before clearing alerts.
        # This prevents rapid online/offline/online cycles from spamming alerts.
        self.miner_stable_online_since = {}  # {miner_name: timestamp} - when miner became stable online
        self.hysteresis_threshold_sec = CONFIG.get("hysteresis_threshold_sec", 120)  # 2 min stable before clearing

        # ═══════════════════════════════════════════════════════════════════════════════
        # CHRONIC ISSUE DETECTION - Track stuck/repeating alerts
        # ═══════════════════════════════════════════════════════════════════════════════
        self.chronic_issues = {}  # {miner_name: {"type": alert_type, "count": N, "first_seen": ts, "last_seen": ts}}

        # ═══════════════════════════════════════════════════════════════════════════════
        # POOL VS MINER HASHRATE TRACKING - Detect divergence
        # ═══════════════════════════════════════════════════════════════════════════════
        self.miner_pool_hashrate = {}  # {miner_name: {"miner_hr": X, "pool_hr": Y, "divergence_count": N}}

        # ═══════════════════════════════════════════════════════════════════════════════
        # P0 AUDIT FIX: ORPHAN DETECTION - Track block statuses to detect orphans
        # ═══════════════════════════════════════════════════════════════════════════════
        # Stores known block hashes and their last-seen status to detect status changes.
        # When a block transitions from "pending"/"confirmed" to "orphaned", alert immediately.
        self.known_block_statuses = {}  # {block_hash: {"status": "confirmed", "height": 12345, "found_at": ts}}
        self.orphan_alerts_sent = set()  # Set of block hashes we've already alerted on

        # ═══════════════════════════════════════════════════════════════════════════════
        # POOL-SIDE BLOCK DETECTION - Detect new blocks for ALL miner types
        # ═══════════════════════════════════════════════════════════════════════════════
        # Tracks block hashes seen from pool API to detect new blocks.
        # This works for ALL miners (including nmaxe, nerdqaxe, avalon, hammer, axeos)
        # since the pool knows when any worker finds a block.
        self.seen_pool_block_hashes = set()  # Set of block hashes we've already processed

        # ═══════════════════════════════════════════════════════════════════════════════
        # SATS SURGE TRACKING - Alert when coin/BTC sat value increases significantly
        # ═══════════════════════════════════════════════════════════════════════════════
        # Tracks sat values (coin price in BTC satoshis) over time to detect surges.
        # When a coin's sat value increases 25%+ over 1 week baseline, send an alert.
        # This helps identify good times to convert mined coins to BTC.
        self.sats_history = {}  # {coin_symbol: [{"ts": timestamp, "sats": sat_value}, ...]}
        self.sats_surge_last_alert = {}  # {coin_symbol: timestamp} - last alert time per coin

        # ═══════════════════════════════════════════════════════════════════════════════
        # THERMAL PROTECTION - Track critical temp duration for sustained shutdown
        # ═══════════════════════════════════════════════════════════════════════════════
        self.thermal_critical_since = {}   # {miner_name: first_critical_timestamp}
        self.thermal_shutdown_sent = {}    # {miner_name: True} prevents re-sending stop command

        # ═══════════════════════════════════════════════════════════════════════════════
        # NEW MONITORING ALERTS - State tracking for B1-B8 alerts
        # ═══════════════════════════════════════════════════════════════════════════════
        self.fan_alert_sent = {}           # {miner_name: True} cleared when fans resume
        self.last_known_orphan_count = 0   # Total orphaned blocks from Prometheus
        self.orphan_count_initialized = False  # First reading is baseline, don't alert
        self.zmq_stale_alerted = False     # Cleared when ZMQ age drops below threshold
        self.worker_count_baseline = []    # Rolling list of last 10 worker count samples
        self.share_loss_alerted = False    # Cleared when share loss rate drops below threshold
        self.last_block_notify_mode = None # Last seen block notification mode (ZMQ vs polling)
        self.last_replica_count = None     # Last seen HA replica count

        # ═══════════════════════════════════════════════════════════════════════════════
        # INFRASTRUCTURE CRITICAL - Circuit breaker, backpressure, WAL, ZMQ socket
        # ═══════════════════════════════════════════════════════════════════════════════
        self.circuit_breaker_alerted = False    # Cleared when state returns to 0 (closed)
        self.backpressure_alerted = False       # Cleared when level drops below 2
        self.last_wal_write_errors = 0          # Track cumulative WAL write error count
        self.last_wal_commit_errors = 0         # Track cumulative WAL commit error count
        self.wal_errors_initialized = False     # First reading is baseline, don't alert
        self.zmq_disconnected_alerted = False   # Cleared when ZMQ reconnects

        # ═══════════════════════════════════════════════════════════════════════════════
        # SECURITY - Stratum URL mismatch detection
        # ═══════════════════════════════════════════════════════════════════════════════
        self.known_miner_pool_urls = {}         # {miner_name: "stratum+tcp://..."} baseline URLs
        self.url_mismatch_alerted = {}          # {miner_name: True} prevents re-alerting

        # ═══════════════════════════════════════════════════════════════════════════════
        # HARDWARE - Hashboard death, HW error rate
        # ═══════════════════════════════════════════════════════════════════════════════
        self.hashboard_alert_sent = {}          # {miner_name: True} cleared when boards recover
        self.miner_hw_errors = {}               # {miner_name: [{"t": ts, "total": N}, ...]} rolling history
        self.hw_error_alert_sent = {}           # {miner_name: True} cleared when rate drops

        # ═══════════════════════════════════════════════════════════════════════════════
        # ECONOMIC - Best share
        # ═══════════════════════════════════════════════════════════════════════════════
        self.best_share_difficulty = 0          # All-time best share difficulty from Prometheus

        # ═══════════════════════════════════════════════════════════════════════════════
        # FINANCIAL - Price crash, payout tracking, revenue velocity
        # ═══════════════════════════════════════════════════════════════════════════════
        self.price_history = {}                # {coin: [{"ts": timestamp, "usd": price}, ...]} 1h rolling
        self.price_crash_last_alert = {}       # {coin: timestamp} cooldown per coin
        self.last_wallet_balance = None        # Last known wallet balance (float)
        self.wallet_balance_last_check = 0     # Timestamp of last wallet balance check
        self.missing_payout_alerted = False    # Cleared when balance changes
        self.payout_deferred_from_quiet = False # True when payout alert was suppressed by quiet hours
        self.previous_month_earnings = {}      # Snapshot of last month's final earnings totals
        self.revenue_decline_alerted = False   # Cleared each new month

    def _new_weekly_stats(self):
        stats = {"network_samples": [], "fleet_samples": [], "odds_samples": [], "blocks_found": 0, "offline_events": 0, "start": time.time()}
        for coin in SUPPORTED_COIN_SYMBOLS:
            stats[f"earned_{coin}"] = 0
        return stats

    def _new_quarterly_stats(self):
        """Create new quarterly stats with multi-coin support (all supported coins)."""
        stats = {f"total_{coin}": 0 for coin in SUPPORTED_COIN_SYMBOLS}
        stats.update({"total_blocks": 0, "offline_events": 0, "uptime_samples": [], "start": time.time()})
        return stats

    def load(self):
        if not self.state_file.exists(): return
        try:
            with open(self.state_file) as f: d = json.load(f)
            for k in self._PERSIST_KEYS:
                if k in d: setattr(self, k, d[k])
            cutoff = time.time() - 604800
            self.network_history = [x for x in self.network_history if x.get("t", 0) > cutoff]
            # Convert list back to set for orphan tracking
            if isinstance(self.orphan_alerts_sent, list):
                self.orphan_alerts_sent = set(self.orphan_alerts_sent)
            # Convert list back to set for pool block tracking
            if isinstance(self.seen_pool_block_hashes, list):
                self.seen_pool_block_hashes = set(self.seen_pool_block_hashes)
            # Migrate earnings dict: ensure keys exist for all current coins.
            # Old state.json may lack keys for coins added after it was saved.
            for _coin in SUPPORTED_COIN_SYMBOLS:
                self.earnings.setdefault(f"monthly_{_coin}", 0)
                self.earnings.setdefault(f"lifetime_{_coin}", 0)
            # Same migration for quarterly stats
            for _coin in SUPPORTED_COIN_SYMBOLS:
                self.quarterly_stats.setdefault(f"total_{_coin}", 0)
            # Migrate high_odds tracking from scalar (old) to per-coin dict (new)
            if not isinstance(self.high_odds_last_alert, dict):
                self.high_odds_last_alert = {}
            if not isinstance(self.high_odds_first_detected, dict):
                self.high_odds_first_detected = {}
            if not hasattr(self, 'high_odds_session_alerted') or not isinstance(self.high_odds_session_alerted, dict):
                self.high_odds_session_alerted = {}
        except (json.JSONDecodeError, IOError, OSError, KeyError, TypeError) as e:
            logger.warning(f"Could not load state file: {e}")

        # Re-seed pool_blocks_found from the database if state.json has a stale
        # (lower) value — handles database restores where historical blocks are
        # imported but state.json still has the old count.
        try:
            _db_blocks = fetch_pool_blocks(limit=10000)
            if _db_blocks:
                db_count = len(_db_blocks)
                if db_count > self.pool_blocks_found:
                    logger.info(f"Pool block counter corrected: state had {self.pool_blocks_found}, DB has {db_count}")
                    self.pool_blocks_found = db_count
        except Exception:
            pass  # API not ready — keep state.json value

    def save(self):
        """Save state to disk atomically.

        H-5 fix: Uses write-to-temp-then-rename pattern to prevent state
        corruption if process crashes during write. The rename operation
        is atomic on POSIX filesystems.
        """
        import tempfile
        d = {k: getattr(self, k) for k in self._PERSIST_KEYS}

        # Convert sets to lists for JSON serialization
        if isinstance(d.get("orphan_alerts_sent"), set):
            d["orphan_alerts_sent"] = list(d["orphan_alerts_sent"])
        if isinstance(d.get("seen_pool_block_hashes"), set):
            d["seen_pool_block_hashes"] = list(d["seen_pool_block_hashes"])

        # Write to temp file in same directory (ensures same filesystem for atomic rename)
        temp_fd, temp_path = tempfile.mkstemp(
            suffix='.tmp',
            prefix='state_',
            dir=str(self.data_dir)
        )
        try:
            with os.fdopen(temp_fd, 'w') as f:
                json.dump(d, f)
                f.flush()
                os.fsync(f.fileno())  # Ensure data is on disk before rename

            # Atomic rename (on POSIX; on Windows this may fail if target exists)
            # Use shutil.move for cross-platform compatibility
            import shutil
            shutil.move(temp_path, str(self.state_file))
        except Exception as e:
            # Clean up temp file on error
            try:
                os.unlink(temp_path)
            except OSError:
                pass
            logger.error(f"Failed to save state atomically: {e}")
            # Don't re-raise — state save failure should not crash the monitor loop

    def prune_stale_miner_state(self, active_miners):
        """Remove state entries for miners that no longer exist.

        Prevents unbounded growth of miner_offline_since, last_alerts (per-miner keys),
        miner_restart_times, temp_alert_sent, miner_last_uptime, and other per-miner dicts.
        Called periodically (e.g. once per hour) with the set of currently-configured miner names.

        Args:
            active_miners: set of miner names currently in the MINERS database
        """
        if not active_miners:
            return

        STALE_AGE = 30 * 86400  # 30 days
        now = time.time()
        pruned = 0

        # Per-miner dicts that accumulate entries
        miner_dicts = [
            "miner_offline_since", "miner_restart_times", "temp_alert_sent",
            "miner_offline_alert_sent", "miner_last_uptime", "miner_health_history", "miner_temp_history",
            "miner_hashrate_history", "miner_uptimes", "miner_block_counts",
            "miner_stale_history", "miner_hashrate_baseline", "miner_stable_online_since",
            "miner_pool_hashrate", "known_miner_pool_urls", "url_mismatch_alerted",
            "hashboard_alert_sent", "miner_hw_errors", "hw_error_alert_sent",
            "fan_alert_sent", "thermal_critical_since", "thermal_shutdown_sent",
            "pool_share_history",
        ]
        for attr_name in miner_dicts:
            d = getattr(self, attr_name, None)
            if not isinstance(d, dict):
                continue
            stale_keys = [k for k in d if k not in active_miners]
            for k in stale_keys:
                del d[k]
                pruned += 1

        # Prune per-miner alert cooldown keys (format: "alert_type:miner_name")
        stale_alert_keys = []
        for k, v in self.last_alerts.items():
            if ":" in k:
                miner_part = k.split(":", 1)[1]
                if miner_part not in active_miners:
                    # Only prune if the alert timestamp is old enough
                    if isinstance(v, (int, float)) and (now - v) > STALE_AGE:
                        stale_alert_keys.append(k)
        for k in stale_alert_keys:
            del self.last_alerts[k]
            pruned += 1

        if pruned > 0:
            logger.info(f"Pruned {pruned} stale miner state entries (active miners: {len(active_miners)})")

    def record_sample(self, net_phs, fleet_ths, odds):
        t = time.time()
        self.network_history.append({"t": t, "phs": net_phs})
        # Prune network_history at runtime — keep 7 days max (matches load() pruning)
        # Only runs the list comprehension when cap is exceeded to minimize overhead
        if len(self.network_history) > 20160:  # ~1 week at 30s intervals
            cutoff = t - 604800
            self.network_history = [x for x in self.network_history if x.get("t", 0) > cutoff]
        self.weekly_stats["network_samples"].append(net_phs)
        self.weekly_stats["fleet_samples"].append(fleet_ths)
        self.weekly_stats["odds_samples"].append(odds["daily_odds_pct"])
        # Safety cap on weekly_stats lists (reset_weekly() handles normal cleanup,
        # but if it fails to fire, these would grow unbounded)
        for key in ("network_samples", "fleet_samples", "odds_samples"):
            if len(self.weekly_stats[key]) > 20160:
                self.weekly_stats[key] = self.weekly_stats[key][-20160:]

    def record_miner_sample(self, name, hashrate_ghs, temp):
        t = time.time()
        if name not in self.miner_hashrate_history: self.miner_hashrate_history[name] = []
        if name not in self.miner_temp_history: self.miner_temp_history[name] = []
        self.miner_hashrate_history[name].append({"t": t, "hr": hashrate_ghs})
        if temp: self.miner_temp_history[name].append({"t": t, "temp": temp})
        cutoff = t - 86400
        self.miner_hashrate_history[name] = [x for x in self.miner_hashrate_history[name] if x["t"] > cutoff][-500:]
        self.miner_temp_history[name] = [x for x in self.miner_temp_history[name] if x["t"] > cutoff][-500:]

    def calc_health_score(self, name, uptime_pct, expected_hr):
        # All components default to 100 for perfect health score when no issues
        components = {"uptime": min(100, uptime_pct), "temp_stability": 100, "hashrate_consistency": 100, "stale_rate": 100}
        temps = [x["temp"] for x in self.miner_temp_history.get(name, [])[-50:]]
        if temps:
            variance = sum((t - sum(temps)/len(temps))**2 for t in temps) / len(temps) if len(temps) > 1 else 0
            std_dev = variance ** 0.5  # Use std dev (°C) not variance (°C²) to avoid over-penalizing
            components["temp_stability"] = max(0, 100 - (std_dev * 5))  # 3°C=85, 10°C=50, 20°C=0
        hrs = [x["hr"] for x in self.miner_hashrate_history.get(name, [])[-50:]]
        if hrs and expected_hr > 0:
            components["hashrate_consistency"] = min(100, (sum(hrs)/len(hrs) / expected_hr) * 100)
        # Compute stale_rate from actual tracked data (excludes stale from rejection count)
        stale_history = self.miner_stale_history.get(name, [])
        if len(stale_history) >= 3:
            recent_sh = stale_history[-10:]
            sh_acc = sum(s.get("accepted", 0) for s in recent_sh)
            sh_rej = sum(s.get("rejected", 0) for s in recent_sh)
            sh_stale = sum(s.get("stale", 0) for s in recent_sh)
            sh_true_rej = max(0, sh_rej - sh_stale)
            sh_total = sh_acc + sh_rej
            if sh_total > 0:
                rej_rate = (sh_true_rej / sh_total) * 100
                components["stale_rate"] = max(0, 100 - (rej_rate * 5))
        score = sum(components[k] * HEALTH_WEIGHTS[k] for k in HEALTH_WEIGHTS) / sum(HEALTH_WEIGHTS.values())
        return score, components

    def track_miner_stales(self, name, accepted, rejected, stale=0):
        if name not in self.miner_stale_history: self.miner_stale_history[name] = []
        prev = self.miner_stale_history[name][-1] if self.miner_stale_history[name] else None
        if prev and (accepted < prev.get("total_accepted", 0)):
            self.miner_stale_history[name] = []
            prev = None
        if prev is None:
            # First sample: store baseline totals only (zero deltas bias zombie/rejection detection)
            self.miner_stale_history[name].append({"t": time.time(), "accepted": 0, "rejected": 0, "stale": 0, "total_accepted": accepted, "total_rejected": rejected, "total_stale": stale, "is_baseline": True})
        else:
            delta_acc = max(0, accepted - prev.get("total_accepted", 0))
            delta_rej = max(0, rejected - prev.get("total_rejected", 0))
            delta_stale = max(0, stale - prev.get("total_stale", 0))
            self.miner_stale_history[name].append({"t": time.time(), "accepted": delta_acc, "rejected": delta_rej, "stale": delta_stale, "total_accepted": accepted, "total_rejected": rejected, "total_stale": stale})
        self.miner_stale_history[name] = self.miner_stale_history[name][-50:]

    def track_pool_shares(self, name, pool_hashrate, shares_per_second):
        """Track pool-side share submissions for a miner"""
        if name not in self.pool_share_history:
            self.pool_share_history[name] = []
        self.pool_share_history[name].append({
            "t": time.time(),
            "hashrate": pool_hashrate,
            "sps": shares_per_second
        })
        # Keep last 30 samples (about 1 hour at 2 min intervals)
        self.pool_share_history[name] = self.pool_share_history[name][-30:]

    # ═══════════════════════════════════════════════════════════════════════════════
    # ALERT BATCHING/AGGREGATION - Reduce Discord spam by combining alerts
    # ═══════════════════════════════════════════════════════════════════════════════
    #
    # Alert batching works in two ways:
    # 1. Per-miner batching: Multiple alerts for the SAME miner are combined
    # 2. Fleet-wide batching: Multiple miners with alerts are combined into a digest
    #
    # Example: If 5 miners go offline within 5 minutes (power event), instead of
    # 5 separate "miner offline" alerts, you get ONE digest showing all 5.
    # ═══════════════════════════════════════════════════════════════════════════════

    # Maximum batch size to prevent unbounded memory growth in pathological scenarios
    MAX_ALERT_BATCH_SIZE = 100

    def queue_alert_for_batching(self, alert_type, embed, miner_name=None):
        """
        Queue an alert for batching instead of sending immediately.

        Args:
            alert_type: The type of alert (e.g., "miner_offline", "temp_warning")
            embed: The Discord embed dict
            miner_name: Optional miner name for miner-specific alerts
        """
        # Flush if batch is getting too large to prevent unbounded memory growth
        if len(self.global_alert_batch) >= self.MAX_ALERT_BATCH_SIZE:
            logger.warning(f"Alert batch reached max size ({self.MAX_ALERT_BATCH_SIZE}), forcing flush")
            self.flush_alert_batch()

        now = time.time()
        self.global_alert_batch.append({
            "type": alert_type,
            "embed": embed,
            "miner": miner_name,
            "time": now
        })
        logger.debug(f"Batched alert: {alert_type} for {miner_name or 'fleet'} (batch size: {len(self.global_alert_batch)})")

    def should_flush_batch(self):
        """Check if we should flush the alert batch."""
        if not self.global_alert_batch:
            return False

        now = time.time()
        oldest_alert = min(a["time"] for a in self.global_alert_batch)

        # Flush if oldest alert is past the window
        return (now - oldest_alert) >= self.alert_context_window

    def flush_alert_batch(self):
        """
        Process and send all batched alerts.
        Combines multiple alerts into digest notifications to reduce spam.

        Returns:
            int: Number of notifications actually sent
        """
        if not self.global_alert_batch:
            return 0

        now = time.time()
        self.last_batch_flush = now

        # Group alerts by type
        alerts_by_type = {}
        for alert in self.global_alert_batch:
            atype = alert["type"]
            if atype not in alerts_by_type:
                alerts_by_type[atype] = []
            alerts_by_type[atype].append(alert)

        notifications_sent = 0

        failed_embeds = []
        for alert_type, alerts in alerts_by_type.items():
            if len(alerts) == 1:
                # Single alert of this type - send as-is
                if send_notifications(alerts[0]["embed"]):
                    notifications_sent += 1
                else:
                    failed_embeds.append(alerts[0]["embed"])
            else:
                # Multiple alerts of same type - create digest
                digest_embed = self._create_alert_digest(alert_type, alerts)
                if send_notifications(digest_embed):
                    notifications_sent += 1
                else:
                    failed_embeds.append(digest_embed)

        # Clear the batch
        batch_size = len(self.global_alert_batch)
        self.global_alert_batch = []

        # Re-queue failed alerts for retry on next flush cycle
        if failed_embeds:
            logger.warning(f"Alert batch: {len(failed_embeds)} notifications failed to send, re-queuing for retry")
            for embed in failed_embeds:
                self.global_alert_batch.append({"type": "retry", "embed": embed, "miner": None, "time": time.time()})

        if batch_size > 1:
            logger.info(f"Alert batch flushed: {batch_size} alerts → {notifications_sent} sent, {len(failed_embeds)} re-queued")

        return notifications_sent

    def _create_alert_digest(self, alert_type, alerts):
        """
        Create a single digest embed from multiple alerts of the same type.

        This dramatically reduces notification spam when multiple miners
        experience the same issue (e.g., power outage, network problem).
        """
        miner_names = [a["miner"] for a in alerts if a["miner"]]
        count = len(alerts)
        first_embed = alerts[0]["embed"]

        # Build appropriate title and description based on alert type
        type_info = {
            "miner_offline": ("🔴", "Miners Offline", "Multiple miners went offline"),
            "miner_online": ("🟢", "Miners Online", "Multiple miners came back online"),
            "miner_reboot": ("🔄", "Miners Rebooted", "Multiple miners rebooted"),
            "temp_warning": ("🌡️", "Temperature Warnings", "Multiple miners have high temperatures"),
            "temp_critical": ("🔥", "CRITICAL Temperatures", "Multiple miners at critical temps!"),
            "degradation": ("📉", "Hashrate Degradation", "Multiple miners showing degraded hashrate"),
            "zombie_miner": ("🧟", "Zombie Miners", "Multiple miners not submitting shares"),
            "excessive_restarts": ("⚠️", "Excessive Restarts", "Multiple miners restarting frequently"),
            "auto_restart": ("🔧", "Auto-Restart Attempts", "Multiple auto-restart attempts made"),
            # New monitoring alerts
            "thermal_shutdown": ("🔥", "Thermal Shutdowns", "Multiple miners hit thermal emergency"),
            "fan_failure": ("🌀", "Fan Failures", "Multiple miners have fan failures"),
            "share_rejection_spike": ("📊", "Rejection Spikes", "Multiple miners with high rejection rates"),
            "zmq_stale": ("📡", "ZMQ Stale", "ZMQ message staleness detected"),
            "worker_count_drop": ("👷", "Worker Drops", "Worker count dropped significantly"),
            "share_loss_rate": ("📉", "Share Loss", "Elevated share loss rate detected"),
            "block_notify_mode_change": ("📡", "Notify Mode Changes", "Block notification mode changed"),
            # Infrastructure, security, hardware, economic alerts
            "circuit_breaker": ("🔴", "Circuit Breaker Open", "Pool circuit breaker triggered"),
            "backpressure": ("🔴", "Backpressure Critical", "Pool buffer overflow detected"),
            "wal_errors": ("💾", "Database Errors", "WAL write/commit failures detected"),
            "zmq_disconnected": ("📡", "ZMQ Disconnected", "ZMQ socket connection lost"),
            "stratum_url_mismatch": ("🔒", "URL Mismatch", "Miner stratum URL changed unexpectedly"),
            "hashboard_dead": ("🪫", "Hashboard Dead", "Multiple miners lost hashboards"),
            "hw_error_rate": ("⚠️", "HW Error Rate", "Multiple miners with high hardware error rates"),
            "best_share": ("🏆", "Best Share", "New all-time best share difficulty"),
            # Financial alerts
            "price_crash": ("📉", "Price Crash", "Significant coin price drop detected"),
            "payout_received": ("💰", "Payout Received", "Wallet balance increased"),
            "missing_payout": ("⚠️", "Missing Payout", "Expected payout not received"),
            "revenue_decline": ("📉", "Revenue Decline", "Mining revenue pace declining"),
        }

        emoji, title_suffix, desc_prefix = type_info.get(alert_type, ("⚠️", "Alerts", "Multiple alerts triggered"))

        # Format miner list
        if len(miner_names) <= 5:
            miner_list = "\n".join(f"  • **{name}**" for name in miner_names)
        else:
            # Show first 4 and count of others
            miner_list = "\n".join(f"  • **{name}**" for name in miner_names[:4])
            miner_list += f"\n  • ... and **{len(miner_names) - 4}** more"

        # Determine color based on severity
        color_map = {
            "miner_offline": COLORS["red"],
            "temp_critical": COLORS["red"],
            "zombie_miner": COLORS["red"],
            "miner_online": COLORS["green"],
            "miner_reboot": COLORS["yellow"],
            "temp_warning": COLORS["yellow"],
            "degradation": COLORS["orange"],
            "excessive_restarts": COLORS["orange"],
            "auto_restart": COLORS["orange"],
            # New monitoring alerts
            "thermal_shutdown": COLORS["red"],
            "fan_failure": COLORS["red"],
            "share_rejection_spike": COLORS["orange"],
            "zmq_stale": COLORS["orange"],
            "worker_count_drop": COLORS["orange"],
            "share_loss_rate": COLORS["orange"],
            "block_notify_mode_change": COLORS["yellow"],
            # Infrastructure, security, hardware, economic alerts
            "circuit_breaker": COLORS["red"],
            "backpressure": COLORS["red"],
            "wal_errors": COLORS["red"],
            "zmq_disconnected": COLORS["red"],
            "stratum_url_mismatch": COLORS["red"],
            "hashboard_dead": COLORS["red"],
            "hw_error_rate": COLORS["orange"],
            "best_share": COLORS["purple"],
            # Financial alerts
            "price_crash": COLORS["red"],
            "payout_received": COLORS["green"],
            "missing_payout": COLORS["orange"],
            "revenue_decline": COLORS["orange"],
        }
        color = color_map.get(alert_type, COLORS["yellow"])

        # Build digest embed
        digest = {
            "title": f"{emoji} Alert Digest: {count} {title_suffix}",
            "description": f"**{desc_prefix}**\n\n{miner_list}",
            "color": color,
            "fields": [
                {"name": "📊 Count", "value": f"**{count}** miners", "inline": True},
                {"name": "⏱️ Window", "value": f"{self.alert_context_window // 60} min", "inline": True},
            ],
            "footer": {"text": f"💡 Batched to reduce spam • Check fleet health"},
            "timestamp": utc_ts()
        }

        # Add relevant fields from first alert (e.g., temperature readings)
        original_fields = first_embed.get("fields", [])
        if original_fields and len(digest["fields"]) < 5:
            # Add up to 2 fields from original
            for field in original_fields[:2]:
                if field.get("name") not in ["📊 Count", "⏱️ Window"]:
                    digest["fields"].append(field)

        return digest

    # Legacy method for backwards compatibility (per-miner batching)
    def queue_alert(self, miner_name, alert_type, embed):
        """Queue an alert for context enrichment (legacy method, now uses global batching)."""
        self.queue_alert_for_batching(alert_type, embed, miner_name)

    def get_enriched_alerts(self):
        """
        Process pending alerts and return enriched/combined alerts.
        Legacy method - now wraps flush_alert_batch for compatibility.
        Returns list of (alert_type, embed, miner_name) tuples to send.
        """
        # This is now handled by flush_alert_batch()
        # Keep for backwards compatibility
        if self.should_flush_batch():
            self.flush_alert_batch()
        return []

    def _create_combined_alert(self, miner_name, alerts):
        """Create a single enriched alert from multiple related alerts for ONE miner."""
        alert_types = [a["type"] for a in alerts]
        first_embed = alerts[0]["embed"]

        # Build combined description
        issues = []
        for a in alerts:
            alert_type = a["type"]
            if alert_type == "miner_offline":
                issues.append("🔴 Went offline")
            elif alert_type == "temp_warning":
                issues.append("🌡️ High temperature warning")
            elif alert_type == "temp_critical":
                issues.append("🔥 CRITICAL temperature!")
            elif alert_type == "miner_reboot":
                issues.append("🔄 Rebooted")
            elif alert_type == "zombie_miner":
                issues.append("🧟 Not submitting shares")
            elif alert_type == "degradation":
                issues.append("📉 Hashrate degraded")
            else:
                issues.append(f"⚠️ {alert_type}")

        # Count unique issues
        unique_count = len(set(alert_types))
        window_min = self.alert_context_window // 60

        return {
            "title": f"⚠️ {miner_name} - {unique_count} Issue{'s' if unique_count > 1 else ''} Detected",
            "description": f"Multiple alerts triggered within {window_min} minutes:\n\n" + "\n".join(f"• {issue}" for issue in issues),
            "color": COLORS["red"],
            "fields": first_embed.get("fields", [])[:3],  # Include some context from first alert
            "footer": {"text": f"💡 Check miner health - possible hardware or connection issue"},
            "timestamp": utc_ts()
        }

    # ═══════════════════════════════════════════════════════════════════════════════
    # CHRONIC ISSUE DETECTION - Flag repeatedly failing miners
    # ═══════════════════════════════════════════════════════════════════════════════

    def track_chronic_issue(self, miner_name, alert_type):
        """Track recurring alerts for the same miner to detect chronic issues."""
        now = time.time()
        key = f"{miner_name}:{alert_type}"

        if key not in self.chronic_issues:
            self.chronic_issues[key] = {
                "count": 1,
                "first_seen": now,
                "last_seen": now,
                "alerted": False
            }
        else:
            issue = self.chronic_issues[key]
            # Reset if issue was resolved (no alert for 2 hours)
            if now - issue["last_seen"] > 7200:
                self.chronic_issues[key] = {
                    "count": 1,
                    "first_seen": now,
                    "last_seen": now,
                    "alerted": False
                }
            else:
                issue["count"] += 1
                issue["last_seen"] = now

    def check_chronic_issues(self, miner_name):
        """
        Check if a miner has chronic (repeated) issues.
        Returns (is_chronic, issue_info) if chronic, (False, None) otherwise.
        """
        now = time.time()
        chronic_threshold = 5  # Alert if same issue fires 5+ times

        for key, issue in self.chronic_issues.items():
            if not key.startswith(f"{miner_name}:"):
                continue

            if issue["count"] >= chronic_threshold and not issue["alerted"]:
                alert_type = key.split(":", 1)[1]
                duration_hours = (now - issue["first_seen"]) / 3600
                issue["alerted"] = True  # Mark as alerted to avoid spam
                return True, {
                    "type": alert_type,
                    "count": issue["count"],
                    "duration_hours": duration_hours
                }

        return False, None

    # ═══════════════════════════════════════════════════════════════════════════════
    # MINER RESTART FREQUENCY DETECTION - Alert on frequent reboots
    # ═══════════════════════════════════════════════════════════════════════════════

    def get_restart_frequency(self, miner_name, window_hours=1):
        """
        Get the number of restarts in the last N hours.
        Returns (count, timestamps_list).
        """
        now = time.time()
        cutoff = now - (window_hours * 3600)

        restarts = self.miner_restart_times.get(miner_name, [])
        if isinstance(restarts, (int, float)):
            # Legacy: single timestamp stored
            restarts = [restarts] if restarts > cutoff else []

        # Handle list of timestamps
        if isinstance(restarts, list):
            recent = [t for t in restarts if t > cutoff]
            return len(recent), recent

        return 0, []

    def get_last_restart_time(self, miner_name):
        """Get the most recent restart time for a miner (handles legacy and list formats)."""
        restarts = self.miner_restart_times.get(miner_name, 0)
        if isinstance(restarts, (int, float)):
            return restarts  # Legacy single timestamp
        elif isinstance(restarts, list) and restarts:
            return max(restarts)  # Most recent from list
        return 0

    def record_miner_restart(self, miner_name):
        """Record a miner restart with proper list tracking."""
        now = time.time()

        if miner_name not in self.miner_restart_times:
            self.miner_restart_times[miner_name] = []
        elif isinstance(self.miner_restart_times[miner_name], (int, float)):
            # Convert legacy single value to list
            self.miner_restart_times[miner_name] = [self.miner_restart_times[miner_name]]

        self.miner_restart_times[miner_name].append(now)

        # Keep only last 24 hours of restarts
        cutoff = now - 86400
        self.miner_restart_times[miner_name] = [
            t for t in self.miner_restart_times[miner_name] if t > cutoff
        ][-50:]  # Cap at 50 entries

        # Clear any pending degradation tracking since miner just restarted
        if miner_name in self.miner_hashrate_baseline:
            self.miner_hashrate_baseline[miner_name]["degradation_start"] = None

    def check_excessive_restarts(self, miner_name, threshold=3, window_hours=1):
        """
        Check if miner has restarted too many times recently.
        Returns (is_excessive, count, window_hours) if excessive.
        """
        count, _ = self.get_restart_frequency(miner_name, window_hours)
        if count >= threshold:
            return True, count, window_hours
        return False, count, window_hours

    # ═══════════════════════════════════════════════════════════════════════════════
    # POOL VS MINER HASHRATE VARIANCE - Detect connectivity/submission issues
    # ═══════════════════════════════════════════════════════════════════════════════

    def track_hashrate_variance(self, miner_name, miner_hr_ghs, pool_hr_ghs):
        """
        Track divergence between miner-reported and pool-reported hashrate.
        Alert if pool sees significantly less hashrate than miner reports.

        Divergence triggers when pool reports <50% of miner's claimed hashrate.
        Recovery is instant: count resets to 0 when ratio recovers above 75%.
        This prevents stale alerts from persisting hours after the issue resolves.
        """
        if miner_name not in self.miner_pool_hashrate:
            self.miner_pool_hashrate[miner_name] = {
                "divergence_count": 0,
                "last_check": 0
            }

        tracker = self.miner_pool_hashrate[miner_name]
        tracker["miner_hr"] = miner_hr_ghs
        tracker["pool_hr"] = pool_hr_ghs
        tracker["last_check"] = time.time()

        # Check for significant divergence (pool reports <50% of miner's claimed hashrate)
        if miner_hr_ghs > 0 and pool_hr_ghs >= 0:
            ratio = pool_hr_ghs / miner_hr_ghs
            if ratio < 0.5:
                tracker["divergence_count"] += 1
            elif ratio >= 0.75:
                # Strong recovery (75%+) — reset immediately to prevent stale alerts.
                # Pool hashrate is statistical; once above 75% the issue is resolved.
                tracker["divergence_count"] = 0
            else:
                # Between 50-75% — slow decrement (improving but not yet healthy)
                tracker["divergence_count"] = max(0, tracker["divergence_count"] - 2)

    def check_hashrate_divergence(self, miner_name, threshold_count=6):
        """
        Check if miner has persistent hashrate divergence (pool sees much less than miner claims).
        Returns (is_divergent, miner_hr, pool_hr, divergence_count) if issue detected.

        Requires 6 consecutive checks (was 3) to filter transient startup fluctuations.
        Only alerts if the CURRENT ratio is still below 60% — prevents confusing alerts
        that show "98% received" from historical divergence that has already resolved.
        """
        tracker = self.miner_pool_hashrate.get(miner_name, {})
        if tracker.get("divergence_count", 0) >= threshold_count:
            miner_hr = tracker.get("miner_hr", 0)
            pool_hr = tracker.get("pool_hr", 0)
            # Only alert if CURRENT ratio is still problematic
            # This prevents "98% received" alerts from stale historical counts
            if miner_hr > 0:
                current_ratio = pool_hr / miner_hr
                if current_ratio >= 0.6:
                    # Current hashrate is fine — issue has resolved, skip alert
                    return False, 0, 0, 0
            return True, miner_hr, pool_hr, tracker["divergence_count"]
        return False, 0, 0, 0

    # ═══════════════════════════════════════════════════════════════════════════════
    # ENHANCED HEALTH SCORE - Include temp trend and restart frequency
    # ═══════════════════════════════════════════════════════════════════════════════

    def calc_enhanced_health_score(self, name, uptime_pct, expected_hr):
        """
        Enhanced health score that includes:
        - Uptime percentage
        - Temperature stability AND trend (rising = bad)
        - Hashrate consistency
        - Stale/reject rate
        - Restart frequency (many restarts = bad)
        """
        components = {
            "uptime": min(100, uptime_pct),
            "temp_stability": 100,
            "temp_trend": 100,  # New: penalize rising temps
            "hashrate_consistency": 100,
            "stale_rate": 95,
            "restart_stability": 100  # New: penalize frequent restarts
        }

        # Temperature stability (standard deviation)
        temps = [x["temp"] for x in self.miner_temp_history.get(name, [])[-50:]]
        if temps:
            variance = sum((t - sum(temps)/len(temps))**2 for t in temps) / len(temps) if len(temps) > 1 else 0
            std_dev = variance ** 0.5  # Use std dev (°C) not variance (°C²) to avoid over-penalizing
            components["temp_stability"] = max(0, 100 - (std_dev * 5))  # 3°C=85, 10°C=50, 20°C=0

            # Temperature trend: compare last 10 samples to first 10
            if len(temps) >= 20:
                early_avg = sum(temps[:10]) / 10
                late_avg = sum(temps[-10:]) / 10
                trend_delta = late_avg - early_avg
                # Rising temp = penalty (each degree of rise = -5 points), falling temp = no bonus
                components["temp_trend"] = min(100, max(0, 100 - (trend_delta * 5)))

        # Hashrate consistency
        hrs = [x["hr"] for x in self.miner_hashrate_history.get(name, [])[-50:]]
        if hrs and expected_hr > 0:
            components["hashrate_consistency"] = min(100, (sum(hrs)/len(hrs) / expected_hr) * 100)

        # Stale/reject rate from tracked history (excludes stale from rejection count)
        stale_history = self.miner_stale_history.get(name, [])
        if len(stale_history) >= 3:
            recent_sh = stale_history[-10:]
            sh_acc = sum(s.get("accepted", 0) for s in recent_sh)
            sh_rej = sum(s.get("rejected", 0) for s in recent_sh)
            sh_stale = sum(s.get("stale", 0) for s in recent_sh)
            sh_true_rej = max(0, sh_rej - sh_stale)
            sh_total = sh_acc + sh_rej
            if sh_total > 0:
                # Each 1% rejection rate = -5 health points (20% reject = 0 score)
                rej_rate = (sh_true_rej / sh_total) * 100
                components["stale_rate"] = max(0, 100 - (rej_rate * 5))

        # Restart stability: penalize frequent restarts
        restart_count, _ = self.get_restart_frequency(name, window_hours=24)
        # Each restart in 24h = -10 points (5+ restarts = 50% penalty)
        components["restart_stability"] = max(0, 100 - (restart_count * 10))

        # Calculate weighted average with updated weights
        enhanced_weights = {
            "uptime": 0.25,
            "temp_stability": 0.15,
            "temp_trend": 0.10,
            "hashrate_consistency": 0.25,
            "stale_rate": 0.15,
            "restart_stability": 0.10
        }

        score = sum(components[k] * enhanced_weights[k] for k in enhanced_weights)
        return score, components

    def check_zombie_miner(self, name):
        """Check if miner is a zombie (online but not submitting valid shares)"""
        if (time.time() - self.get_last_restart_time(name)) < 900: return None  # 15 min cooldown

        # Method 1: Check miner-reported shares (wider window to avoid false positives)
        # Exclude stale shares — only true rejections indicate a zombie miner
        # Skip baseline samples (first sample after restart has zero deltas)
        history = self.miner_stale_history.get(name, [])
        if len(history) >= 15:
            recent = [s for s in history[-15:] if not s.get("is_baseline")]
            if len(recent) >= 10:
                total_acc = sum(s.get("accepted", 0) for s in recent)
                total_rej = sum(s.get("rejected", 0) for s in recent)
                total_stale = sum(s.get("stale", 0) for s in recent)
                true_rej = max(0, total_rej - total_stale)
                total = total_acc + total_rej
                if total == 0:
                    return {"status": "no_shares", "reason": "No new shares in 15 cycles (miner-reported)"}
                if total > 0 and true_rej / total >= 0.9:
                    return {"status": "zombie", "reason": f"{true_rej/total*100:.0f}% reject rate (miner-reported)"}

        # Method 2: Check pool-side share verification (more reliable)
        # NOTE: Only works when pool supports per-worker stats. If pool only returns
        # wallet-level aggregate (like Spiral Pool), pool_share_history won't have fresh data.
        if CONFIG.get("pool_share_validation", True):
            pool_history = self.pool_share_history.get(name, [])
            if len(pool_history) >= 5:
                recent_pool = pool_history[-5:]
                # Skip if pool data is stale (>10 min old) - means pool doesn't support per-worker
                newest_entry_time = max(s.get("t", 0) for s in recent_pool)
                if time.time() - newest_entry_time > 600:  # Data older than 10 min = stale
                    pass  # Skip Method 2 - pool data is stale (pool likely doesn't support per-worker)
                else:
                    # Check if pool sees any hashrate from this miner
                    avg_pool_hr = sum(s.get("hashrate", 0) for s in recent_pool) / len(recent_pool) if recent_pool else 0
                    avg_sps = sum(s.get("sps", 0) for s in recent_pool) / len(recent_pool) if recent_pool else 0

                    # If pool sees zero hashrate for 5+ cycles but miner claims online
                    if avg_pool_hr == 0 and avg_sps == 0:
                        # Safety check: If pool history has ALWAYS been zero, it likely means
                        # we can't match this miner to pool stats (name mismatch), not that
                        # the miner is actually a zombie. Skip to avoid false positives.
                        all_history_zero = all(
                            s.get("hashrate", 0) == 0 and s.get("sps", 0) == 0
                            for s in pool_history
                        )
                        if all_history_zero and len(pool_history) >= 10:
                            # Never saw any pool data for this miner - likely matching issue
                            pass  # Skip zombie check - can't verify this miner against pool
                        else:
                            # Double-check: is the miner actually online?
                            miner_hr_history = self.miner_hashrate_history.get(name, [])
                            if miner_hr_history and len(miner_hr_history) >= 3:
                                recent_miner_hr = [x.get("hr", 0) for x in miner_hr_history[-3:]]
                                if sum(recent_miner_hr) / len(recent_miner_hr) > 0:
                                    # Miner claims to be hashing but pool sees nothing
                                    return {"status": "pool_invisible", "reason": "Pool sees no shares (check stratum connection)"}

        return None

    def update_hashrate_baseline(self, name, hashrate_ghs):
        if hashrate_ghs <= 0 or (time.time() - self.get_last_restart_time(name)) < 900: return None  # 15 min cooldown
        if name not in self.miner_hashrate_baseline:
            self.miner_hashrate_baseline[name] = {"avg": hashrate_ghs, "samples": 1, "last_alert": 0, "degradation_start": None}
            return None
        baseline = self.miner_hashrate_baseline[name]
        is_outlier = baseline["samples"] >= 5 and hashrate_ghs < baseline["avg"] * 0.5
        is_degraded = baseline["samples"] >= 10 and hashrate_ghs < baseline["avg"] * 0.8
        if not is_outlier and not is_degraded:
            # Only incorporate into baseline when hashrate is within normal range (>80% of baseline)
            # This prevents gradual baseline drift during sustained degradation
            samples = min(baseline["samples"], 100)
            baseline["avg"] = (baseline["avg"] * samples + hashrate_ghs) / (samples + 1)
            baseline["samples"] = samples + 1
        # Always check degradation (even for outliers — especially for outliers)
        samples = baseline["samples"]
        if samples >= 10:
            drop_pct = ((baseline["avg"] - hashrate_ghs) / baseline["avg"]) * 100
            if drop_pct >= 20:
                # Track sustained degradation - only alert if degraded for 10+ minutes
                if baseline.get("degradation_start") is None:
                    baseline["degradation_start"] = time.time()
                elif (time.time() - baseline["degradation_start"]) >= 600 and (time.time() - baseline.get("last_alert", 0)) > 3600:
                    baseline["last_alert"] = time.time()
                    baseline["degradation_start"] = None  # Reset after alerting
                    return {"name": name, "current": hashrate_ghs, "baseline": baseline["avg"], "drop_pct": drop_pct}
            else:
                baseline["degradation_start"] = None  # Recovered, reset tracking
        return None

    def record_blip(self, name):
        now = time.time()
        self.recent_blips.append({"t": now, "miner": name})
        self.recent_blips = [b for b in self.recent_blips if now - b["t"] < 300]

    def check_power_event(self):
        now = time.time()
        recent = [b for b in self.recent_blips if now - b["t"] < 60]
        if len(recent) >= 2:
            miners = list(set(b["miner"] for b in recent))
            self.recent_blips = [b for b in self.recent_blips if now - b["t"] >= 60]
            return {"miners": miners, "count": len(miners)}
        return None

    def record_miner_status(self, name, is_online):
        if name not in self.miner_uptimes: self.miner_uptimes[name] = {"total": 0, "online": 0, "last": time.time()}
        elapsed = time.time() - self.miner_uptimes[name].get("last", time.time())
        self.miner_uptimes[name]["total"] += elapsed
        if is_online: self.miner_uptimes[name]["online"] += elapsed
        self.miner_uptimes[name]["last"] = time.time()

    def get_uptime(self, name):
        data = self.miner_uptimes.get(name, {"total": 0, "online": 0})
        return (data["online"] / data["total"] * 100) if data["total"] > 0 else 100

    def check_new_blocks(self, miner_blocks, network_phs, odds, coin_symbol=None):
        new_miners = []
        for name, cur in miner_blocks.items():
            prev = self.miner_block_counts.get(name, 0)
            if cur > prev:
                new_miners.append({"miner": name, "new": cur - prev, "total": cur})
                self.miner_block_counts[name] = cur
                self.record_block(name, cur, network_phs, odds["daily_odds_pct"], coin_symbol)
        return new_miners

    def record_block(self, miner, block_num, network_phs, odds, coin_symbol=None):
        """Record a block found. Supports all 14 coins including aux chains: NMC, SYS, XMY, FBTC."""
        coin = coin_symbol.upper() if coin_symbol else (get_primary_coin() or "UNKNOWN")
        self.block_history.append({
            "t": time.time(), "miner": miner, "height": block_num,
            "network_phs": network_phs, "odds": odds, "coin": coin
        })
        self.block_history = self.block_history[-100:]

        # Get coin-specific reward
        bri = fetch_block_reward_for_coin(coin)
        reward = bri.get("sha256_reward", bri.get("scrypt_reward", DEFAULT_BLOCK_REWARDS.get(coin, 0))) if bri else DEFAULT_BLOCK_REWARDS.get(coin, 0)

        # Update per-coin earnings
        coin_key = coin.lower()
        monthly_key = f"monthly_{coin_key}"
        lifetime_key = f"lifetime_{coin_key}"

        if monthly_key in self.earnings:
            self.earnings[monthly_key] += reward
        if lifetime_key in self.earnings:
            self.earnings[lifetime_key] += reward

        self.earnings["monthly_blocks"] += 1
        self.earnings["lifetime_blocks"] += 1
        self.weekly_stats["blocks_found"] += 1
        weekly_earn_key = f"earned_{coin_key}"
        if weekly_earn_key in self.weekly_stats:
            self.weekly_stats[weekly_earn_key] += reward
        self.record_quarterly_block(reward, coin)

    def check_for_orphans(self, pool_id=None):
        """P0 AUDIT FIX: Check pool API for orphaned blocks and alert immediately.

        This method:
        1. Fetches recent blocks from pool API
        2. Compares current status to previously seen status
        3. Detects when a block transitions to "orphaned" status
        4. Returns list of newly orphaned blocks for alerting

        Args:
            pool_id: Optional pool ID to query. If None, uses pool_id from config.

        Returns:
            List of dicts with orphan info: [{"hash": "abc", "height": 123, "coin": "DGB", "found_at": ts}, ...]
        """
        newly_orphaned = []

        try:
            # Fetch recent blocks from pool API
            pool_blocks = fetch_pool_blocks(limit=50, pool_id=pool_id)
            if not pool_blocks:
                return []

            for block in pool_blocks:
                block_hash = block.get("hash", "")
                if not block_hash:
                    continue

                current_status = block.get("status", "").lower()
                block_height = block.get("blockHeight", 0)
                block_coin = block.get("coin", get_primary_coin() or "UNKNOWN")
                found_at = block.get("created", "")
                block_reward = block.get("reward", 0)

                # Track this block if we haven't seen it before
                if block_hash not in self.known_block_statuses:
                    self.known_block_statuses[block_hash] = {
                        "status": current_status,
                        "height": block_height,
                        "coin": block_coin,
                        "found_at": found_at,
                        "reward": block_reward,
                        "first_seen": time.time()
                    }
                    continue

                # Check for status transition TO orphaned
                prev_status = self.known_block_statuses[block_hash].get("status", "")
                if current_status == "orphaned" and prev_status != "orphaned":
                    # Block just became orphaned!
                    if block_hash not in self.orphan_alerts_sent:
                        newly_orphaned.append({
                            "hash": block_hash,
                            "height": block_height,
                            "coin": block_coin,
                            "found_at": found_at,
                            "orphaned_at": time.time(),
                            "prev_status": prev_status,
                            "reward": self.known_block_statuses[block_hash].get("reward", block_reward)
                        })
                        self.orphan_alerts_sent.add(block_hash)
                        logger.warning(f"ORPHAN DETECTED: Block {block_height} ({block_hash[:16]}...) status: {prev_status} -> orphaned")

                # Update tracked status
                self.known_block_statuses[block_hash]["status"] = current_status

            # Prune old entries (keep last 100 blocks)
            if len(self.known_block_statuses) > 100:
                # Sort by first_seen time and keep newest 100
                sorted_hashes = sorted(
                    self.known_block_statuses.keys(),
                    key=lambda h: self.known_block_statuses[h].get("first_seen", 0),
                    reverse=True
                )[:100]
                self.known_block_statuses = {h: self.known_block_statuses[h] for h in sorted_hashes}

            # Keep orphan_alerts_sent in sync with known_block_statuses — always prune
            # so stale hashes evicted from known_block_statuses can't block future detection
            self.orphan_alerts_sent = self.orphan_alerts_sent & set(self.known_block_statuses.keys())

        except Exception as e:
            logger.debug(f"check_for_orphans error: {e}")

        return newly_orphaned

    def check_pool_for_new_blocks(self, network_phs, odds, coin_symbol=None, pool_id=None):
        """Pool-side block detection for ALL miner types.

        This method detects new blocks by querying the pool API, which works for
        ALL miners regardless of type (including nmaxe, nerdqaxe, avalon, hammer, axeos)
        since the pool knows when any connected worker finds a block.

        Unlike miner-reported found_blocks (which only works for industrial ASICs),
        this method catches blocks found by small miners that don't reliably report
        block counts.

        Args:
            network_phs: Network hashrate in PH/s
            odds: Odds dict with daily_odds_pct etc.
            coin_symbol: Coin symbol for this block query
            pool_id: Optional pool ID to query. If None, uses pool_id from config.

        Returns:
            List of dicts with new block info: [{"miner": "worker", "height": 123, "hash": "abc", "coin": "DGB"}, ...]
        """
        new_blocks = []

        try:
            # Fetch recent blocks from pool API
            pool_blocks = fetch_pool_blocks(limit=20, pool_id=pool_id)
            if not pool_blocks:
                return []

            wallet = CONFIG.get("wallet_address", "")
            _fresh_start = len(self.seen_pool_block_hashes) == 0

            for block in pool_blocks:
                block_hash = block.get("hash", "")
                if not block_hash:
                    continue

                # Skip blocks we've already processed
                if block_hash in self.seen_pool_block_hashes:
                    continue

                # On fresh state (state.json lost or first run), seed ALL existing blocks
                # as seen without alerting. We have no record of what was already alerted,
                # so re-alerting anything risks duplicates. Normal restarts with intact
                # state.json have seen_pool_block_hashes populated (fresh_start=False) and
                # handle missed blocks correctly via the normal path below.
                if _fresh_start:
                    self.seen_pool_block_hashes.add(block_hash)
                    logger.info(f"Fresh state: seeding block {block.get('blockHeight', '?')} as seen (no alert)")
                    continue

                # Check if this block belongs to our wallet/workers
                block_miner = block.get("miner", "")
                # Prefer 'source' (worker name from stratum), fall back to 'miner' (wallet address)
                block_worker = block.get("source", "") or block.get("miner", "unknown")

                # Only process blocks from our wallet
                if wallet and block_miner and not block_miner.startswith(wallet):
                    # Not our block - mark as seen but don't alert
                    self.seen_pool_block_hashes.add(block_hash)
                    continue

                # Extract worker name (strip wallet prefix if present)
                worker_name = block_worker
                if "." in block_worker:
                    worker_name = block_worker.split(".")[-1]

                block_height = block.get("blockHeight", 0)
                block_coin = block.get("coin", coin_symbol or get_primary_coin() or "UNKNOWN")
                block_status = block.get("status", "pending").lower()

                # Only alert on pending/confirmed blocks (not orphaned)
                if block_status not in ["pending", "confirmed"]:
                    self.seen_pool_block_hashes.add(block_hash)
                    continue

                # New block found!
                new_blocks.append({
                    "miner": worker_name,
                    "height": block_height,
                    "hash": block_hash,
                    "coin": block_coin,
                    "status": block_status,
                    "created": block.get("created", ""),
                    "reward": block.get("reward", 0)
                })

                # Record this block
                self.record_block(worker_name, block_height, network_phs, odds.get("daily_odds_pct", 0), block_coin)
                self.seen_pool_block_hashes.add(block_hash)

                logger.info(f"POOL BLOCK DETECTED: Block {block_height} by {worker_name} ({block_coin})")

            # Prune old entries — cross-reference with block_history timestamps
            # Only keep hashes that appear in recent block_history (last 100 blocks)
            if len(self.seen_pool_block_hashes) > 1000:
                recent_hashes = {b.get("hash", "") for b in self.block_history if b.get("hash")}
                if recent_hashes:
                    self.seen_pool_block_hashes = self.seen_pool_block_hashes & recent_hashes
                # If still over limit (no hash data in block_history), keep most recent 500
                if len(self.seen_pool_block_hashes) > 1000:
                    all_hashes = sorted(self.seen_pool_block_hashes)
                    self.seen_pool_block_hashes = set(all_hashes[-500:])

        except Exception as e:
            logger.debug(f"check_pool_for_new_blocks error: {e}")

        return new_blocks

    def get_extremes(self, hours=6):
        cutoff = time.time() - (hours * 3600)
        samples = [x for x in self.network_history if x["t"] > cutoff]
        if not samples: return None
        best = min(samples, key=lambda x: x["phs"])
        worst = max(samples, key=lambda x: x["phs"])
        display_tz = get_display_tz()
        return {"best": {"phs": best["phs"], "time": datetime.fromtimestamp(best["t"], tz=display_tz).strftime("%H:%M")}, "worst": {"phs": worst["phs"], "time": datetime.fromtimestamp(worst["t"], tz=display_tz).strftime("%H:%M")}}

    def get_weekly_summary(self):
        s = self.weekly_stats
        n = max(len(s.get("network_samples", [1])), 1)
        return {"blocks_found": s.get("blocks_found", 0), "offline_events": s.get("offline_events", 0), "avg_network_phs": sum(s.get("network_samples", [0]))/n, "avg_fleet_ths": sum(s.get("fleet_samples", [0]))/n, "avg_odds": sum(s.get("odds_samples", [0]))/n}

    def get_weekly_earnings(self, prices=None):
        """Get weekly earnings with multi-coin support — matches weekly block count window."""
        all_prices = fetch_all_prices() or {}
        result = {}
        for coin in SUPPORTED_COIN_SYMBOLS:
            result[coin] = self.weekly_stats.get(f"earned_{coin}", 0)
        result["blocks"] = self.weekly_stats.get("blocks_found", 0)
        per_coin = {}
        for cur_code in CURRENCY_CODES:
            cur_total = 0
            for coin in SUPPORTED_COIN_SYMBOLS:
                coin_fiat = self.weekly_stats.get(f"earned_{coin}", 0) * all_prices.get(f"{coin}_{cur_code}", 0)
                per_coin[f"{coin}_{cur_code}"] = coin_fiat
                cur_total += coin_fiat
            result[cur_code] = cur_total
        result["per_coin"] = per_coin
        return result

    def get_daily_summary(self):
        """Get today's block and earnings summary for the night report."""
        now = local_now()
        today_start = now.replace(hour=0, minute=0, second=0, microsecond=0)
        today_ts = today_start.timestamp()

        blocks_today = [b for b in self.block_history if b.get("t", 0) >= today_ts]

        # Sum rewards by coin
        coin_rewards = {}
        for b in blocks_today:
            coin = b.get("coin", "UNKNOWN")
            reward = DEFAULT_BLOCK_REWARDS.get(coin, 0)
            coin_rewards[coin] = coin_rewards.get(coin, 0) + reward

        return {
            "blocks": len(blocks_today),
            "coin_rewards": coin_rewards,
            "miners": list(set(b.get("miner", "?") for b in blocks_today))
        }

    def get_monthly_earnings(self, prices=None):
        """Get monthly earnings with multi-coin support - ALL 14 supported coins."""
        all_prices = fetch_all_prices() or {}

        # Build result with per-coin amounts
        result = {}
        for coin in SUPPORTED_COIN_SYMBOLS:
            result[coin] = self.earnings.get(f"monthly_{coin}", 0)
        result["blocks"] = self.earnings["monthly_blocks"]

        # Calculate fiat totals for all supported currencies using loop
        per_coin = {}
        for cur_code in CURRENCY_CODES:
            cur_total = 0
            for coin in SUPPORTED_COIN_SYMBOLS:
                coin_fiat = self.earnings.get(f"monthly_{coin}", 0) * all_prices.get(f"{coin}_{cur_code}", 0)
                per_coin[f"{coin}_{cur_code}"] = coin_fiat
                cur_total += coin_fiat
            result[cur_code] = cur_total

        result["per_coin"] = per_coin
        return result

    def reset_weekly(self):
        self.weekly_stats = self._new_weekly_stats()

    def reset_monthly(self):
        """Reset monthly earnings for all supported coins."""
        # Snapshot previous month's earnings for revenue velocity comparison
        all_prices = fetch_all_prices() or {}
        prev_usd = 0
        for coin in SUPPORTED_COIN_SYMBOLS:
            amount = self.earnings.get(f"monthly_{coin}", 0)
            usd_key = f"{coin}_usd"
            prev_usd += amount * all_prices.get(usd_key, 0)
        elapsed = time.time() - self.earnings.get("monthly_start", time.time())
        days_in_month = max(elapsed / 86400, 1)
        self.previous_month_earnings = {
            "total_usd": prev_usd,
            "days": days_in_month,
            "daily_rate_usd": prev_usd / days_in_month,
            "blocks": self.earnings.get("monthly_blocks", 0),
        }
        self.revenue_decline_alerted = False  # Reset for new month
        for coin in SUPPORTED_COIN_SYMBOLS:
            self.earnings[f"monthly_{coin}"] = 0
        self.earnings["monthly_blocks"] = 0
        self.earnings["monthly_start"] = time.time()

    def get_quarterly_summary(self):
        """Get summary for quarterly report with multi-coin support."""
        s = self.quarterly_stats
        uptime_samples = s.get("uptime_samples", [])
        avg_uptime = sum(uptime_samples) / len(uptime_samples) if uptime_samples else 100
        result = {f"total_{coin}": s.get(f"total_{coin}", 0) for coin in SUPPORTED_COIN_SYMBOLS}
        result.update({
            "total_blocks": s.get("total_blocks", 0),
            "offline_events": s.get("offline_events", 0),
            "avg_uptime": avg_uptime,
            "start": s.get("start", time.time()),
        })
        return result

    def reset_quarterly(self):
        """Reset quarterly stats"""
        self.quarterly_stats = self._new_quarterly_stats()

    def record_quarterly_block(self, reward, coin_symbol=None):
        """Track blocks in quarterly stats with multi-coin support."""
        coin = coin_symbol.upper() if coin_symbol else (get_primary_coin() or "UNKNOWN")
        coin_key = f"total_{coin.lower()}"
        if coin_key in self.quarterly_stats:
            self.quarterly_stats[coin_key] += reward
        self.quarterly_stats["total_blocks"] += 1

    def record_quarterly_uptime(self, uptime_pct):
        """Track uptime samples for quarterly average"""
        self.quarterly_stats["uptime_samples"].append(uptime_pct)
        # Keep last 1000 samples
        self.quarterly_stats["uptime_samples"] = self.quarterly_stats["uptime_samples"][-1000:]

    def record_quarterly_offline(self):
        """Track offline events in quarterly stats"""
        self.quarterly_stats["offline_events"] += 1

    def update_personal_bests(self, fleet_ths, daily_odds_pct, all_online=False):
        """Update personal bests tracking. Returns dict of any new records broken."""
        new_records = {}
        now = local_now().strftime("%Y-%m-%d %H:%M")

        # Highest fleet hashrate
        if fleet_ths > self.personal_bests.get("highest_fleet_ths", 0):
            self.personal_bests["highest_fleet_ths"] = fleet_ths
            self.personal_bests["highest_fleet_ths_date"] = now
            new_records["highest_fleet_ths"] = fleet_ths

        # Highest daily odds
        if daily_odds_pct > self.personal_bests.get("highest_daily_odds_pct", 0):
            self.personal_bests["highest_daily_odds_pct"] = daily_odds_pct
            self.personal_bests["highest_daily_odds_date"] = now
            new_records["highest_daily_odds_pct"] = daily_odds_pct

        return new_records

    def record_block_time(self):
        """Record time since last block for fastest block tracking"""
        if len(self.block_history) >= 2:
            last_two = self.block_history[-2:]
            time_between = last_two[1]["t"] - last_two[0]["t"]
            fastest = self.personal_bests.get("fastest_block_seconds")
            if fastest is None or time_between < fastest:
                self.personal_bests["fastest_block_seconds"] = time_between
                self.personal_bests["fastest_block_date"] = local_now().strftime("%Y-%m-%d %H:%M")
                return time_between
        return None

    def update_uptime_streak(self, all_miners_online, total_miners):
        """Track longest all-miners-online uptime streak.

        Uses persisted uptime_streak_start in personal_bests so streaks survive restarts.
        """
        if all_miners_online and total_miners > 0:
            # Get streak start time from persisted state (survives restarts)
            streak_start = self.personal_bests.get("uptime_streak_start")
            if streak_start is None:
                # First time tracking - start the streak now
                streak_start = time.time()
                self.personal_bests["uptime_streak_start"] = streak_start

            streak_hours = (time.time() - streak_start) / 3600
            if streak_hours > self.personal_bests.get("longest_uptime_streak_hours", 0):
                self.personal_bests["longest_uptime_streak_hours"] = streak_hours
                self.personal_bests["longest_uptime_streak_date"] = local_now().strftime("%Y-%m-%d")
        else:
            # Reset streak when any miner goes offline
            self.personal_bests["uptime_streak_start"] = time.time()

    def record_hashrate_sample(self, fleet_ths):
        """Record hashrate sample for 24h trend tracking"""
        now = time.time()
        self.hashrate_history_24h.append({"t": now, "ths": fleet_ths})
        # Keep 24 hours of samples
        cutoff = now - 86400
        self.hashrate_history_24h = [x for x in self.hashrate_history_24h if x["t"] > cutoff]

    def get_hashrate_trend(self):
        """Get 24h hashrate trend: 'up', 'down', or 'stable'"""
        if len(self.hashrate_history_24h) < 10:
            return "stable", 0

        # Compare first third to last third of samples
        samples = self.hashrate_history_24h
        third = len(samples) // 3
        if third < 3:
            return "stable", 0

        early_avg = sum(x["ths"] for x in samples[:third]) / third
        late_avg = sum(x["ths"] for x in samples[-third:]) / third

        if early_avg == 0:
            return "stable", 0

        pct_change = ((late_avg - early_avg) / early_avg) * 100

        if pct_change > 5:
            return "up", pct_change
        elif pct_change < -5:
            return "down", pct_change
        return "stable", pct_change

    def get_personal_bests_summary(self):
        """Get formatted personal bests for reports"""
        pb = self.personal_bests
        lines = []

        if pb.get("highest_fleet_ths", 0) > 0:
            lines.append(f"⚡ Peak: **{pb['highest_fleet_ths']:.2f} TH/s**")

        if pb.get("fastest_block_seconds"):
            hours = pb["fastest_block_seconds"] / 3600
            if hours < 24:
                lines.append(f"🏃 Fastest: **{hours:.1f}h** between blocks")
            else:
                days = hours / 24
                lines.append(f"🏃 Fastest: **{days:.1f}d** between blocks")

        if pb.get("longest_uptime_streak_hours", 0) > 0:
            hours = pb["longest_uptime_streak_hours"]
            if hours < 24:
                lines.append(f"💪 Streak: **{hours:.1f}h** all rigs online")
            else:
                days = hours / 24
                lines.append(f"💪 Streak: **{days:.1f}d** all rigs online")

        if pb.get("highest_daily_odds_pct", 0) > 0:
            lines.append(f"🎰 Best Odds: **{pb['highest_daily_odds_pct']:.1f}%**")

        return "\n".join(lines) if lines else None

    # ═══════════════════════════════════════════════════════════════════════════════
    # SATS SURGE TRACKING METHODS - Track coin/BTC sat values over time
    # ═══════════════════════════════════════════════════════════════════════════════

    def record_sats_sample(self, all_prices):
        """Record current sat values for all tracked coins.

        Args:
            all_prices: Dict from fetch_all_prices() containing *_sats keys
        """
        if not all_prices:
            return

        now = time.time()
        lookback_days = CONFIG.get("sats_surge_lookback_days", 7)
        max_age = lookback_days * 86400 * 2  # Keep 2x lookback period for safety

        # Track sats only for coins the user has enabled (non-BTC, since sats = coin/BTC ratio)
        enabled_symbols = {c.get("symbol", "").lower() for c in get_enabled_coins()}
        tracked_coins = [c for c in SUPPORTED_COIN_SYMBOLS if c != "btc" and c in enabled_symbols]

        for coin in tracked_coins:
            sats_key = f"{coin}_sats"
            sats_value = all_prices.get(sats_key, 0)

            if sats_value <= 0:
                continue  # Skip if no price data

            # Initialize history for this coin if needed
            if coin not in self.sats_history:
                self.sats_history[coin] = []

            # Add new sample
            self.sats_history[coin].append({"ts": now, "sats": sats_value})

            # Prune old samples (keep only those within max_age)
            cutoff = now - max_age
            self.sats_history[coin] = [s for s in self.sats_history[coin] if s["ts"] > cutoff]

    def check_sats_surge(self):
        """Check if any coin's sat value has surged significantly.

        Returns:
            list: List of dicts with coin surge info, or empty list if no surges
                  Each dict: {coin, current_sats, baseline_sats, change_pct, baseline_date}
        """
        surges = []
        now = time.time()

        threshold_pct = CONFIG.get("sats_surge_threshold_pct", 25)
        lookback_days = CONFIG.get("sats_surge_lookback_days", 7)
        cooldown_hours = CONFIG.get("sats_surge_cooldown_hours", 24)
        lookback_seconds = lookback_days * 86400
        cooldown_seconds = cooldown_hours * 3600

        # Defense-in-depth: only alert for currently enabled coins.
        enabled = get_enabled_coins()
        enabled_symbols = {c.get("symbol", "").lower() for c in enabled if isinstance(c, dict)}
        if not enabled_symbols:
            return surges  # Can't determine enabled coins — skip rather than alert everything

        for coin, history in self.sats_history.items():
            if coin not in enabled_symbols:
                continue  # Skip coins that aren't enabled
            if len(history) < 2:
                continue  # Need at least 2 samples

            # Get current (most recent) sat value
            current = history[-1]
            current_sats = current["sats"]

            # Find baseline sample from ~lookback_days ago
            # Look for the oldest sample within the lookback window
            target_time = now - lookback_seconds
            baseline = None

            for sample in history:
                # Find sample closest to target_time (but at least that old)
                if sample["ts"] <= target_time:
                    if baseline is None or sample["ts"] > baseline["ts"]:
                        baseline = sample

            # If no sample old enough, use the oldest available
            if baseline is None and len(history) >= 24:  # Require at least 24 hours of data
                baseline = history[0]

            if baseline is None:
                continue  # Not enough history yet

            baseline_sats = baseline["sats"]
            if baseline_sats <= 0:
                continue

            # Calculate percentage change
            change_pct = ((current_sats - baseline_sats) / baseline_sats) * 100

            # Check if surge exceeds threshold
            if change_pct >= threshold_pct:
                # Check cooldown - don't re-alert for same coin too soon
                last_alert = self.sats_surge_last_alert.get(coin, 0)
                if (now - last_alert) < cooldown_seconds:
                    continue  # Still in cooldown

                # Record this alert time
                self.sats_surge_last_alert[coin] = now

                surges.append({
                    "coin": coin.upper(),
                    "current_sats": current_sats,
                    "baseline_sats": baseline_sats,
                    "change_pct": change_pct,
                    "baseline_date": datetime.fromtimestamp(baseline["ts"], tz=timezone.utc),
                    "lookback_days": lookback_days
                })

        return surges

    def record_price_sample(self, all_prices):
        """Record current fiat prices for crash detection.

        Args:
            all_prices: Dict from fetch_all_prices() containing *_usd keys
        """
        if not all_prices:
            return

        now = time.time()
        max_age = 7200  # Keep 2 hours of history (enough for 1h lookback + buffer)

        # Only track prices for coins we're actually mining (no alerts for unrelated coins)
        # IMPORTANT: Do NOT fall back to all coins when detection fails — that causes
        # spurious price crash alerts for coins the user hasn't installed (e.g., CAT).
        enabled = get_enabled_coins()
        enabled_symbols = {c.get("symbol", "").lower() for c in enabled if isinstance(c, dict)}
        if not enabled_symbols:
            return  # Can't determine enabled coins — skip rather than track everything
        for coin in enabled_symbols:
            usd_key = f"{coin}_usd"
            usd_price = all_prices.get(usd_key, 0)
            if usd_price <= 0:
                continue

            if coin not in self.price_history:
                self.price_history[coin] = []

            self.price_history[coin].append({"ts": now, "usd": usd_price})

            # Prune old samples
            cutoff = now - max_age
            self.price_history[coin] = [s for s in self.price_history[coin] if s["ts"] > cutoff]

    def check_price_crash(self):
        """Check if any coin's fiat price has crashed significantly (default 15%+ drop in 1 hour).

        Returns:
            list: List of dicts with crash info, or empty list
                  Each dict: {coin, current_usd, baseline_usd, drop_pct}
        """
        crashes = []
        now = time.time()
        crash_pct = PRICE_CRASH_PCT
        cooldown = ALERT_COOLDOWNS.get("price_crash", 14400)

        # Defense-in-depth: only alert for currently enabled coins.
        # price_history may contain stale entries from previous fallback behaviour.
        enabled = get_enabled_coins()
        enabled_symbols = {c.get("symbol", "").lower() for c in enabled if isinstance(c, dict)}
        if not enabled_symbols:
            return crashes  # Can't determine enabled coins — skip rather than alert everything

        for coin, history in self.price_history.items():
            if coin not in enabled_symbols:
                continue  # Skip coins that aren't enabled
            if len(history) < 2:
                continue

            current = history[-1]
            current_usd = current["usd"]

            # Find sample from ~1 hour ago
            target_time = now - 3600
            baseline = None
            for sample in history:
                if sample["ts"] <= target_time:
                    if baseline is None or sample["ts"] > baseline["ts"]:
                        baseline = sample

            # If no sample at least 1 hour old, use oldest available if > 30 min old
            if baseline is None:
                oldest = history[0]
                if (now - oldest["ts"]) >= 1800:
                    baseline = oldest

            if baseline is None:
                continue

            baseline_usd = baseline["usd"]
            if baseline_usd <= 0:
                continue

            # Calculate drop percentage (negative = price went down)
            drop_pct = ((baseline_usd - current_usd) / baseline_usd) * 100

            if drop_pct >= crash_pct:
                # Check per-coin cooldown
                last_alert = self.price_crash_last_alert.get(coin, 0)
                if (now - last_alert) < cooldown:
                    continue

                self.price_crash_last_alert[coin] = now

                crashes.append({
                    "coin": coin.upper(),
                    "current_usd": current_usd,
                    "baseline_usd": baseline_usd,
                    "drop_pct": drop_pct,
                })

        return crashes

    def check_revenue_velocity(self):
        """Check if current month's earnings pace is significantly below last month.

        Returns:
            dict or None: {current_pace, previous_month, decline_pct, days_elapsed}
            Values are in user's preferred currency (REPORT_CURRENCY).
        """
        prev = self.previous_month_earnings
        if not prev or prev.get("daily_rate_usd", 0) <= 0:
            return None  # No previous month data

        if self.revenue_decline_alerted:
            return None  # Already alerted this month

        # Calculate current month's pace in user's preferred currency
        all_prices = fetch_all_prices() or {}
        cur = get_currency_meta()
        fiat_code = cur["code"]

        current_fiat = 0
        for coin in SUPPORTED_COIN_SYMBOLS:
            amount = self.earnings.get(f"monthly_{coin}", 0)
            price_key = f"{coin}_{fiat_code}"
            current_fiat += amount * all_prices.get(price_key, 0)

        elapsed = time.time() - self.earnings.get("monthly_start", time.time())
        days_elapsed = max(elapsed / 86400, 0.5)

        # Need at least 3 days of data to avoid noise
        if days_elapsed < 3:
            return None

        current_daily_rate = current_fiat / days_elapsed

        # Convert previous month's USD rate to target currency
        prev_daily_rate = prev["daily_rate_usd"]
        if fiat_code != "usd":
            # Approximate conversion: derive exchange rate from BTC prices
            btc_usd = all_prices.get("btc_usd", 0)
            btc_target = all_prices.get(f"btc_{fiat_code}", 0)
            if btc_usd > 0 and btc_target > 0:
                prev_daily_rate *= (btc_target / btc_usd)

        if prev_daily_rate <= 0:
            return None

        # Calculate decline
        decline_pct = ((prev_daily_rate - current_daily_rate) / prev_daily_rate) * 100

        if decline_pct >= REVENUE_DECLINE_PCT:
            # Extrapolate what last month looked like at this point
            prev_at_this_point = prev_daily_rate * days_elapsed
            self.revenue_decline_alerted = True
            return {
                "current_pace": current_fiat,
                "previous_month": prev_at_this_point,
                "decline_pct": decline_pct,
                "days_elapsed": int(days_elapsed),
            }

        return None


def _build_simpleswap_surge_field(coin_symbol):
    """Build a Discord embed field with a SimpleSwap link for the surging coin.

    The link opens SimpleSwap with the source coin and BTC pre-selected.
    The operator enters their BTC address on the SimpleSwap website —
    nothing is stored in Spiral Pool config.

    Args:
        coin_symbol: Coin ticker string (e.g. "DGB", "LTC")

    Returns:
        dict suitable for the embed 'fields' list
    """
    coin_lower = coin_symbol.lower()
    swap_url = f"https://simpleswap.io/exchange?from={coin_lower}&to=btc"

    value = (
        f"🔄 [**Convert {coin_symbol} → BTC on SimpleSwap**]({swap_url})\n"
        f"*Click the link — enter your BTC wallet address on the SimpleSwap website.*"
    )

    return {"name": "💱 SimpleSwap", "value": value, "inline": False}


def create_sats_surge_embed(surge_info, all_prices=None):
    """Create Discord embed for sat value surge alert.

    Args:
        surge_info: Dict with coin, current_sats, baseline_sats, change_pct, baseline_date
        all_prices: Optional dict from fetch_all_prices() for fiat price display
    """
    coin = surge_info["coin"]
    current_sats = surge_info["current_sats"]
    baseline_sats = surge_info["baseline_sats"]
    change_pct = surge_info["change_pct"]
    baseline_date = surge_info["baseline_date"]
    lookback_days = surge_info.get("lookback_days", 7)

    coin_emoji = get_coin_emoji(coin)
    coin_name = get_coin_name(coin)

    # Format sat values nicely
    if current_sats >= 1000000:
        current_str = f"{current_sats/1e6:.2f}M sats"
        baseline_str = f"{baseline_sats/1e6:.2f}M sats"
    elif current_sats >= 1000:
        current_str = f"{current_sats/1e3:.1f}K sats"
        baseline_str = f"{baseline_sats/1e3:.1f}K sats"
    else:
        current_str = f"{current_sats:,} sats"
        baseline_str = f"{baseline_sats:,} sats"

    # Format baseline date
    baseline_str_date = baseline_date.strftime("%b %d")

    # Fiat price line if available
    fiat_line = ""
    if all_prices:
        cur = get_currency_meta()
        coin_price = all_prices.get(f"{coin.lower()}_{cur['code']}", 0)
        if coin_price > 0:
            fiat_line = f"\n**Current Price:** `{cur['symbol']}{coin_price:,.{cur['decimals']}f} {REPORT_CURRENCY}`"

    desc = f"""```diff
+ {coin} SAT VALUE UP {change_pct:.1f}% vs {lookback_days}d ago!
```
{coin_emoji} **{coin_name}** {theme("sats_surge.body")}

**Current:** `{current_str}`
**{lookback_days}d Baseline ({baseline_str_date}):** `{baseline_str}`
**Change:** `+{change_pct:.1f}%`{fiat_line}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = []

    # Magnitude-aware swap recommendation
    if change_pct >= 100:
        recommendation = f"🚀 **Recommend to swap.** {coin} has more than doubled in BTC value — strong opportunity to convert to BTC."
    elif change_pct >= 50:
        recommendation = f"📈 **Recommend to swap.** {coin} is up {change_pct:.0f}% vs BTC — good time to convert while the ratio is favorable."
    else:
        recommendation = f"**Recommend to swap.** {coin} sat value is elevated +{change_pct:.0f}% vs {lookback_days}d ago — consider converting to BTC."
    fields.append({"name": "💡 Recommendation", "value": recommendation, "inline": False})

    fields.append(_build_simpleswap_surge_field(coin))

    return _embed(
        theme("sats_surge.title", coin_emoji=coin_emoji, coin_name=coin_name),
        desc,
        COLORS["green"],
        fields,
        footer=f"🌀 Spiral Sentinel v{__version__} • {theme('sats_surge.footer')}"
    )


def create_price_crash_embed(crash_info, all_prices=None):
    """Create Discord embed for coin price crash alert.

    Args:
        crash_info: Dict with coin, current_usd, baseline_usd, drop_pct
        all_prices: Optional dict from fetch_all_prices() for currency conversion
    """
    coin = crash_info["coin"]
    current_usd = crash_info["current_usd"]
    baseline_usd = crash_info["baseline_usd"]
    drop_pct = crash_info["drop_pct"]

    coin_name = get_coin_name(coin)
    coin_emoji = get_coin_emoji(coin)

    # Determine display currency and convert if needed
    cur = get_currency_meta()
    currency_label = REPORT_CURRENCY
    current_price = current_usd
    baseline_price = baseline_usd

    if all_prices and cur["code"] != "usd":
        coin_lc = coin.lower()
        target_now = all_prices.get(f"{coin_lc}_{cur['code']}", 0)
        usd_now = all_prices.get(f"{coin_lc}_usd", 0)
        if target_now > 0 and usd_now > 0:
            rate = target_now / usd_now
            current_price = current_usd * rate
            baseline_price = baseline_usd * rate

    sym = cur["symbol"]

    # Format prices based on magnitude
    if current_price >= 1:
        cur_str = f"{sym}{current_price:,.2f}"
        base_str = f"{sym}{baseline_price:,.2f}"
    elif current_price >= 0.01:
        cur_str = f"{sym}{current_price:.4f}"
        base_str = f"{sym}{baseline_price:.4f}"
    else:
        cur_str = f"{sym}{current_price:.6f}"
        base_str = f"{sym}{baseline_price:.6f}"

    desc = f"""```diff
- {coin} PRICE DOWN {drop_pct:.1f}% IN THE LAST HOUR
```

{coin_emoji} **{coin_name}** {theme("price_crash.body")}

**Current Price:** `{cur_str} {currency_label}`
**1h Ago:** `{base_str} {currency_label}`
**Drop:** `-{drop_pct:.1f}%`

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "⚠️ Impact on Mining",
            "value": f"Your {coin} mining revenue has dropped proportionally.\nCheck if mining is still profitable at current rates.",
            "inline": False
        },
    ]

    return _embed(
        theme("price_crash.title", coin=coin),
        desc,
        COLORS["red"],
        fields,
        footer=theme("price_crash.footer", threshold=PRICE_CRASH_PCT)
    )


def create_payout_received_embed(coin, amount_change, new_balance, prices=None, deferred=False):
    """Create Discord embed for payout received (wallet balance increase)."""
    coin_emoji = get_coin_emoji(coin)
    coin_name = get_coin_name(coin)

    # Format the amount based on coin type
    if coin in ("BTC", "BCH"):
        amount_str = f"{amount_change:.8f} {coin}"
        balance_str = f"{new_balance:.8f} {coin}"
    else:
        amount_str = f"{amount_change:,.2f} {coin}"
        balance_str = f"{new_balance:,.2f} {coin}"

    # Calculate fiat value if prices available
    value_lines = ""
    if prices:
        cur = get_currency_meta()
        fiat_val = amount_change * prices.get(cur["code"], 0)
        if fiat_val > 0:
            value_lines += f"\n**Payout Value:** `{cur['symbol']}{fiat_val:,.{cur['decimals']}f} {REPORT_CURRENCY}`"

    # Note if this payout was deferred from quiet hours
    deferred_note = ""
    if deferred:
        deferred_note = "\n\n> *Payout completed earlier but alert was suppressed during quiet hours.*"

    desc = f"""```diff
+ PAYOUT RECEIVED: {amount_str}
```

{coin_emoji} **{coin_name}** {theme("payout.body")}

**Amount:** `{amount_str}`
**New Balance:** `{balance_str}`{value_lines}{deferred_note}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    return _embed(
        theme("payout.title", coin=coin),
        desc,
        COLORS["green"],
        [],
        footer=f"🌀 Spiral Sentinel v{__version__} • {theme('payout.footer')}"
    )


def create_missing_payout_embed(coin, days_since_change, last_balance):
    """Create Discord embed for missing expected payout."""
    coin_emoji = get_coin_emoji(coin)
    coin_name = get_coin_name(coin)

    if coin in ("BTC", "BCH"):
        balance_str = f"{last_balance:.8f} {coin}"
    else:
        balance_str = f"{last_balance:,.2f} {coin}"

    desc = f"""```fix
NO PAYOUT IN {days_since_change} DAYS
```

{coin_emoji} **{coin_name}** {theme("missing_payout.body", days=days_since_change)}

**Current Balance:** `{balance_str}`

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🔧 Possible Causes",
            "value": "• Pool payout threshold not met\n• Wrong wallet address configured\n• Pool payout processing delayed\n• Firmware hijack redirecting payouts\n• Check your pool dashboard for pending balance",
            "inline": False
        },
    ]

    return _embed(
        theme("missing_payout.title", coin=coin),
        desc,
        COLORS["orange"],
        fields,
        footer=f"🌀 Spiral Sentinel v{__version__} • {theme('missing_payout.footer', days=days_since_change)}"
    )


def create_wallet_drop_embed(coin, amount_dropped, previous_balance, current_balance, prices=None):
    """Create Discord embed for unexpected wallet balance decrease."""
    coin_emoji = get_coin_emoji(coin)
    coin_name = get_coin_name(coin)

    if coin in ("BTC", "BCH"):
        drop_str = f"{amount_dropped:.8f} {coin}"
        prev_str = f"{previous_balance:.8f} {coin}"
        curr_str = f"{current_balance:.8f} {coin}"
    else:
        drop_str = f"{amount_dropped:,.2f} {coin}"
        prev_str = f"{previous_balance:,.2f} {coin}"
        curr_str = f"{current_balance:,.2f} {coin}"

    drop_pct = (amount_dropped / previous_balance * 100) if previous_balance > 0 else 0

    value_lines = ""
    if prices:
        cur = get_currency_meta()
        drop_fiat = amount_dropped * prices.get(cur["code"], 0)
        if drop_fiat > 0:
            value_lines += f"\n**Value Lost:** `{cur['symbol']}{drop_fiat:,.{cur['decimals']}f} {REPORT_CURRENCY}`"

    desc = f"""```diff
- WALLET BALANCE DECREASED: {drop_str}
```

{coin_emoji} **{coin_name}** {theme("wallet_drop.body")} (**{drop_pct:.1f}%**)

**Previous:** `{prev_str}`
**Current:** `{curr_str}`
**Lost:** `{drop_str}`{value_lines}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    fields = [
        {
            "name": "🔧 Possible Causes",
            "value": "• Intentional withdrawal or transfer\n• Transaction fees (unlikely this amount)\n• Wallet compromise or unauthorized access\n• Block reward clawback after reorg\n• Check transaction history immediately",
            "inline": False
        },
    ]

    return _embed(
        theme("wallet_drop.title", coin=coin),
        desc,
        COLORS["red"],
        fields,
        footer=f"🌀 Spiral Sentinel v{__version__} • {theme('wallet_drop.footer')}"
    )


def create_revenue_decline_embed(current_pace, previous_month, decline_pct, days_elapsed, context=None):
    """Create Discord embed for revenue velocity decline.

    Values are in user's preferred currency (from REPORT_CURRENCY config).
    context: Optional dict with price_change_pct, diff_change_pct, hashrate_change_pct, blocks_this_month, blocks_last_month
    """
    cur = get_currency_meta()
    currency_label = REPORT_CURRENCY
    sym = cur["symbol"]

    desc = f"""```diff
- REVENUE PACE DOWN {decline_pct:.0f}% vs LAST MONTH
```

Your mining revenue pace this month is **{decline_pct:.0f}%** below last month at the same point.

**This Month (day {days_elapsed}):** `{sym}{current_pace:,.2f} {currency_label}`
**Last Month (day {days_elapsed}):** `{sym}{previous_month:,.2f} {currency_label}`

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"""

    # Build cause analysis from context if available
    causes = []
    if context:
        if context.get("price_change_pct") is not None and context["price_change_pct"] < -5:
            causes.append(f"📉 Coin price dropped `{context['price_change_pct']:.0f}%`")
        if context.get("diff_change_pct") is not None and context["diff_change_pct"] > 5:
            causes.append(f"📈 Network difficulty up `{context['diff_change_pct']:.0f}%`")
        if context.get("hashrate_change_pct") is not None and context["hashrate_change_pct"] < -5:
            causes.append(f"⚡ Fleet hashrate down `{context['hashrate_change_pct']:.0f}%`")
        if context.get("blocks_this_month") is not None and context.get("blocks_last_month") is not None:
            if context["blocks_this_month"] < context["blocks_last_month"]:
                causes.append(f"🏆 Fewer blocks: `{context['blocks_this_month']}` vs `{context['blocks_last_month']}` last month")
    if not causes:
        causes = [
            "• Coin price dropped",
            "• Network difficulty increased",
            "• Fleet hashrate decreased",
            "• Fewer blocks found",
            "• Orphaned blocks eating revenue",
        ]

    fields = [
        {
            "name": "🔧 Likely Causes" if context else "🔧 Possible Causes",
            "value": "\n".join(causes),
            "inline": False
        },
    ]

    return _embed(
        theme("revenue_decline.title"),
        desc,
        COLORS["orange"],
        fields,
        footer=f"🌀 Spiral Sentinel v{__version__} • Revenue velocity tracker • {REVENUE_DECLINE_PCT}%+ decline threshold"
    )


# === MAIN LOOP ===
def monitor_loop(state):
    # Show startup banner (coin info will be displayed after pool health check)
    logger.info("=" * 65)
    logger.info(f"  Spiral Sentinel v{__version__}")
    logger.info("  Autonomous Solo Mining Monitor")
    logger.info("  CONVERGENT SPIRAL EDITION")
    logger.info("=" * 65)
    logger.info(f"Interval: {CHECK_INTERVAL}s | Alerts: {'ON' if ALERTS_ENABLED else 'OFF'} | Health: {'ON' if HEALTH_MONITORING_ENABLED else 'OFF'}")

    # Show notification channel status
    if DISCORD_WEBHOOK_URL and "YOUR" not in DISCORD_WEBHOOK_URL:
        logger.info(f"Discord: Configured (webhook ...{DISCORD_WEBHOOK_URL[-8:]})")
    else:
        logger.warning(f"Discord: NOT CONFIGURED - set discord_webhook_url in {CONFIG_FILE}")
    if TELEGRAM_ENABLED:
        logger.info("Telegram: Configured")
    if XMPP_ENABLED:
        logger.info("XMPP: Configured")
    if NTFY_ENABLED:
        # Log topic URL without auth token
        ntfy_display = NTFY_URL.split("?")[0]
        logger.info(f"ntfy: Configured ({ntfy_display})")
    if SMTP_ENABLED:
        logger.info(f"Email: Configured ({SMTP_HOST}:{SMTP_PORT} → {', '.join(SMTP_TO)})")

    # Initialize HA manager for alert coordination in multi-node setups
    # This ensures only the MASTER Sentinel sends alerts to prevent triple-alerting
    ha_mgr = init_ha_manager()
    if ha_mgr:
        role = get_ha_role()
        if role != "STANDALONE":
            role_emoji = {"MASTER": "👑", "BACKUP": "🔄", "OBSERVER": "👁️"}.get(role, "❓")
            logger.info(f"{role_emoji} HA Role: {role} (only MASTER sends alerts)")
        else:
            logger.info("Mode: Standalone (no HA cluster detected)")
    else:
        logger.info("Mode: Standalone (HA manager not available)")

    # Start Telegram bot command polling thread (background, daemon — exits with main process)
    if TELEGRAM_COMMANDS_ENABLED:
        tg_cmd_thread = threading.Thread(
            target=_telegram_command_loop, args=(state,), daemon=True, name="tg-cmd-poll"
        )
        tg_cmd_thread.start()
        logger.info("Telegram: Bot commands enabled (/status /miners /hashrate /blocks /help)")

    # Start Sentinel health endpoint (loopback HTTP, GET /health + GET /cooldowns)
    global _health_state_ref
    _health_state_ref = state
    if SENTINEL_HEALTH_ENABLED:
        health_thread = threading.Thread(
            target=_health_server_loop, args=(SENTINEL_HEALTH_PORT,), daemon=True, name="sentinel-health"
        )
        health_thread.start()

    # Initialize historical data manager for long-term trend tracking
    history = HistoricalDataManager()
    primary_coin = get_primary_coin()  # Get configured primary coin
    stats = history.get_summary_stats(coin=primary_coin)
    all_coins = stats.get('all_coins', [])
    if all_coins:
        logger.info(f"Historical data for {stats['coin']}: {stats['difficulty']['samples']} difficulty samples, {stats['network_phs']['samples']} network samples")
        if len(all_coins) > 1:
            logger.info(f"Multi-coin history available: {', '.join(all_coins)}")
    else:
        logger.info("Historical data: No samples yet")

    # 30 minute grace period - allows miners to stabilize and node to sync
    # Prevents restart loops when pool infrastructure is coming online
    STARTUP_GRACE_PERIOD = 1800
    startup_time = time.time()
    logger.info(f"Startup grace: {STARTUP_GRACE_PERIOD//60} min (no auto-restarts during this period)")

    # Set global startup time for alert suppression
    # This suppresses non-critical alerts during the startup window to prevent alert spam
    global SENTINEL_STARTUP_TIME
    SENTINEL_STARTUP_TIME = startup_time
    logger.info(f"Alert suppression: {STARTUP_ALERT_SUPPRESSION_MINUTES} min (non-critical alerts suppressed during startup)")

    # Wait for blockchain to be FULLY synced before starting monitoring
    # Sentinel should not run until the blockchain is ready - there's no point monitoring
    # miners if the pool can't provide accurate block/difficulty data
    #
    # Sync detection uses TWO methods:
    # 1. Pool API health check (blockHeight > 0, networkDifficulty > 0)
    # 2. Node RPC verificationprogress >= 99.99% (handles float precision)
    #
    # The 99.99% threshold matches the rest of the codebase:
    # - install.sh bash scripts (>= 0.9999)
    # - dashboard.py is_synced check (>= 0.9999)
    # - check_coin_node_synced() in this file (>= 0.9999)
    SYNC_THRESHOLD = 0.9999  # 99.99% - matches bash scripts and dashboard

    pool_api_url = CONFIG.get("pool_api_url", "http://localhost:4000")
    logger.info("=" * 65)
    logger.info("  Waiting for blockchain sync to complete...")
    logger.info("  Sentinel will activate once the node is fully synced (99.99%).")
    logger.info("=" * 65)
    logger.info(f"Pool API: {pool_api_url}")

    pool_wait_start = time.time()
    pool_ready = False
    last_status_msg = None
    last_log_time = 0
    last_progress_log = 0
    _sync_wait_reports_sent = set()  # Track reports sent during sync wait to avoid duplicates

    def get_node_sync_progress():
        """Get sync progress from node RPC (verificationprogress 0.0-1.0).
        Checks all enabled coins and returns the highest progress."""
        try:
            # Try to get RPC port from enabled coins config
            coins = get_enabled_coins()
            max_progress = 0
            max_coin = None

            for coin in coins:
                rpc_port = coin.get("rpc_port")
                if not rpc_port:
                    continue
                try:
                    req = urllib.request.Request(
                        f"http://127.0.0.1:{rpc_port}",
                        data=json.dumps({"method": "getblockchaininfo", "params": [], "id": 1}).encode(),
                        headers={"Content-Type": "application/json"}
                    )
                    with urllib.request.urlopen(req, timeout=5) as resp:
                        result = json.loads(resp.read().decode())
                        if "result" in result:
                            progress = result["result"].get("verificationprogress", 0)
                            if progress > max_progress:
                                max_progress = progress
                                max_coin = coin.get("symbol", "UNKNOWN")
                except (urllib.error.URLError, urllib.error.HTTPError, socket.timeout, OSError):
                    continue

            if max_coin:
                return max_progress, max_coin
            return None, None
        except Exception:
            return None, None

    while not pool_ready:
        elapsed = int(time.time() - pool_wait_start)
        elapsed_min = elapsed // 60
        elapsed_sec = elapsed % 60

        # Check pool health (basic check: API responding, blockHeight > 0, difficulty > 0)
        health = check_pool_health()

        # Also check actual node sync progress via RPC
        sync_progress, sync_coin = get_node_sync_progress()

        # Determine if fully synced:
        # - Pool must be healthy (API responding with valid data)
        # - Node must report verificationprogress >= 99.9% OR pool health implies sync
        #
        # IMPORTANT: If pool is healthy (blockHeight > 0, networkDifficulty > 0),
        # the node MUST be synced - the pool gets this data from the node.
        # Direct RPC check may fail if node requires authentication, so we trust
        # pool health as sufficient evidence of sync when RPC is unreachable.
        pool_healthy = health["healthy"]
        node_synced = sync_progress is not None and sync_progress >= SYNC_THRESHOLD

        # Trust pool health as sync indicator when direct RPC fails
        # Pool can't have valid blockHeight/difficulty without a synced node
        pool_implies_sync = pool_healthy and health["block_height"] > 0 and health["network_diff"] > 0

        if pool_healthy and (node_synced or pool_implies_sync):
            pool_ready = True
            if sync_progress is not None:
                sync_pct = sync_progress * 100
                sync_source = f"{sync_pct:.4f}% ({sync_coin})"
            else:
                sync_source = "inferred from pool API (RPC auth required)"
            logger.info("=" * 65)
            logger.info("  Blockchain sync complete!")
            logger.info("=" * 65)
            logger.info(f"Sync progress: {sync_source}")
            logger.info(f"Block height: {health['block_height']:,}")
            logger.info(f"Network difficulty: {format_difficulty(health['network_diff'])}")
            logger.info(f"Connected miners: {health['miners']}")
        else:
            current_time = time.time()

            # Build status message
            if not pool_healthy:
                status_msg = health["reason"]
            elif sync_progress is None and not pool_implies_sync:
                status_msg = "Unable to query node RPC (waiting for pool health)"
            elif sync_progress is None:
                status_msg = "Pool healthy, verifying sync..."
            else:
                sync_pct = sync_progress * 100
                status_msg = f"Syncing: {sync_pct:.2f}%"

            # Log status every 30 seconds or on status change
            if status_msg != last_status_msg or (current_time - last_log_time) >= 30:
                if elapsed_min > 0:
                    time_str = f"{elapsed_min}m {elapsed_sec}s"
                else:
                    time_str = f"{elapsed_sec}s"
                logger.info(f"Waiting for sync... {status_msg} [elapsed: {time_str}]")
                last_status_msg = status_msg
                last_log_time = current_time

            # Every 5 minutes, log a reminder that we're still waiting
            if (current_time - last_progress_log) >= 300:
                last_progress_log = current_time
                if elapsed_min >= 5:
                    logger.info(f"Still waiting for blockchain sync ({elapsed_min} minutes elapsed). This is normal for initial sync.")
                    logger.info("Sentinel will automatically activate once sync reaches 99.99%.")

            # ── Scheduled report fallback during sync wait ──────────────
            # Even when the pool is offline, fire scheduled reports so the
            # user is never left wondering why their intel report is missing.
            # Sends a one-time "pool offline" notification per report hour.
            # Covers both regular REPORT_HOURS and FINAL_REPORT_TIME windows.
            if ENABLE_6H_REPORTS and REPORT_FREQUENCY not in ("off", "daily"):
                now = local_now()
                report_key = None
                if now.hour in REPORT_HOURS and now.minute < REPORT_WINDOW:
                    report_key = now.strftime("%Y-%m-%d-") + str(now.hour)
                elif FINAL_REPORT_TIME:
                    fh, fm = FINAL_REPORT_TIME
                    if now.hour == fh and fm <= now.minute < fm + FINAL_REPORT_WINDOW:
                        report_key = now.strftime("%Y-%m-%d-") + f"final-{fh}:{fm:02d}"
                if report_key and report_key not in _sync_wait_reports_sent:
                        _sync_wait_reports_sent.add(report_key)
                        hrs = elapsed_min // 60
                        mins = elapsed_min % 60
                        dur_str = f"{hrs}h {mins}m" if hrs > 0 else f"{mins}m"
                        outage_embed = _embed(
                            "\u26a0\ufe0f Scheduled Report \u2014 Pool Offline",
                            (f"The scheduled **{now.strftime('%I:%M %p')}** intel report could not be "
                             f"generated because the pool API has been unreachable for **{dur_str}**.\n\n"
                             f"Sentinel is waiting for the pool to come back online. "
                             f"Full reports will resume automatically once connectivity is restored."),
                            0xFF6600,  # Orange warning color
                            fields=[
                                {"name": "\u23f1\ufe0f Outage Duration", "value": f"`{dur_str}`", "inline": True},
                                {"name": "\U0001f50d Status", "value": f"`{status_msg}`", "inline": True},
                            ],
                        )
                        logger.info(f"Sending scheduled report fallback (pool offline): {report_key}")
                        send_notifications(outage_embed)

            time.sleep(5)  # Check every 5 seconds

    logger.info("Pool fully operational - Sentinel coming online")

    # Now that pool is healthy, detect coin information
    # This must happen AFTER pool health check to ensure API is responding
    primary_coin = get_primary_coin()
    primary_coin_name = get_coin_name(primary_coin)
    primary_coin_emoji = get_coin_emoji(primary_coin)
    enabled_coins = get_enabled_coins()
    coin_list = "/".join([c.get("symbol", "?") for c in enabled_coins]) if len(enabled_coins) > 1 else primary_coin
    logger.info(f"{primary_coin_emoji} Primary coin: {primary_coin_name} ({primary_coin})")

    # Push device hints to pool on startup (helps with ESP-Miner classification)
    if CONFIG.get("push_device_hints", True):
        logger.info("Pushing device hints to pool...")
        pushed = push_all_device_hints()
        if pushed > 0:
            logger.info(f"Device hints pushed for {pushed} miners")
        else:
            logger.debug("No device hints to push (or push failed)")

    logger.info("All systems ready - notifications enabled")

    # Force reload miners after startup to catch any miners added during install
    # This fixes timing issues where miner scan runs after Sentinel module loads
    logger.info("Reloading miner database (post-startup sync)...")
    reload_miners()
    miner_count = sum(len(v) for v in MINERS.values())
    logger.info(f"Miner database loaded: {miner_count} miners configured")

    # Check if dashboard setup is complete and provide helpful guidance
    if miner_count == 0:
        dashboard_setup_complete = check_dashboard_setup_complete()
        if not dashboard_setup_complete:
            logger.warning("=" * 65)
            logger.warning("  ⚠️  SETUP REQUIRED: No miners configured")
            logger.warning("=" * 65)
            logger.warning("  Spiral Sentinel monitors miners but none are configured yet.")
            logger.warning("")
            logger.warning("  To add miners, complete one of these steps:")
            logger.warning("  1. Open Spiral Dash (http://your-pool:1618) and complete setup")
            logger.warning("     - The setup wizard will scan your network for miners")
            logger.warning("  2. Run the CLI scanner: spiralpool-scan --subnet 192.168.1.0/24")
            logger.warning("  3. Manually edit: /spiralpool/data/miners.json")
            logger.warning("")
            logger.warning("  Sentinel will auto-detect when miners are added.")
            logger.warning("  Device hints for vardiff will be pushed once miners are configured.")
            logger.warning("=" * 65)
        else:
            logger.warning("No miners in database - add miners via Spiral Dash or spiralpool-scan")

    # Track last update check time
    last_update_check = 0
    last_notified_version = None

    # Track last stale state cleanup (hourly)
    last_stale_cleanup = 0

    # Track last coin change check time
    last_coin_check = 0

    # Track last device hints push
    last_device_hints_push = time.time()

    # Track last infrastructure health check (Prometheus metrics)
    last_infra_check = 0
    INFRA_CHECK_INTERVAL = CONFIG.get("metrics_fetch_interval", 60)  # Default 60 seconds

    # Track last sats surge sample and check
    last_sats_sample = 0
    SATS_SAMPLE_INTERVAL = CONFIG.get("sats_surge_sample_interval", 3600)  # Default 1 hour
    SATS_SURGE_ENABLED = CONFIG.get("sats_surge_enabled", True)

    # Track last payout check and revenue velocity check
    last_payout_check = 0
    last_revenue_check = 0
    REVENUE_CHECK_INTERVAL = 86400  # Check revenue velocity once per day

    # First report cycle flag - skip catch-up on initial startup
    # This prevents sending a "missed" report immediately when Sentinel first starts
    first_report_cycle = True

    # Initialize coin change tracking with current coin
    global LAST_DETECTED_COIN
    LAST_DETECTED_COIN = primary_coin

    net = fetch_network_stats(primary_coin)
    fleet_ths, md, temps, miner_status, power, uptimes, mblocks, mpools, mstats, worker_names, fans, chain_data, hw_errors_data = get_total_hashrate()

    # Always send startup alert - even if network stats fail
    if net:
        odds = calc_odds(net["network_phs"], fleet_ths, primary_coin)
        send_alert("startup_summary", create_startup_embed(fleet_ths, md, temps, miner_status, net["network_phs"], odds, primary_coin, power), state)
    else:
        # Send startup alert without network stats
        logger.warning("Network stats unavailable, sending startup alert without odds")
        send_alert("startup_summary", create_startup_embed(fleet_ths, md, temps, miner_status, None, None, primary_coin, power), state)

    # ── Config validation warning (fire once at startup if issues found) ─────────
    try:
        _cfg_issues = validate_config(CONFIG)
        if _cfg_issues:
            logger.warning(f"Sending config warning embed ({len(_cfg_issues)} issue(s))")
            send_notifications(create_config_warning_embed(_cfg_issues))
    except Exception as _e:
        logger.debug(f"Config warning check error (non-critical): {_e}")

    # ═══════════════════════════════════════════════════════════════════════════════
    # BLOCK RECOVERY — Detect blocks found while Sentinel was offline
    # ═══════════════════════════════════════════════════════════════════════════════
    # When the pool restarts, WAL-reconciled blocks enter the DB immediately.
    # Without this check, block celebrations are delayed until the first normal
    # monitoring cycle, causing "PAYOUT RECEIVED" to fire before "BLOCK CAPTURED".
    try:
        logger.info("Scanning for unclaimed blocks while Sentinel was offline...")
        net_phs_startup = net["network_phs"] if net else 0
        startup_odds = calc_odds(net_phs_startup, fleet_ths, primary_coin) if net else {}
        startup_prices = fetch_coin_price(primary_coin)
        startup_bri = fetch_block_reward_for_coin(primary_coin)
        startup_new_blocks = state.check_pool_for_new_blocks(net_phs_startup, startup_odds, primary_coin)
        if startup_new_blocks:
            logger.info(f"SIGNAL ACQUIRED: {len(startup_new_blocks)} block(s) found while offline — initiating celebration protocol!")
            for block in startup_new_blocks:
                worker = block.get("worker") or block["miner"]
                state.pool_blocks_found += 1
                state.record_block_time()
                miner_details = {}
                for mname, hr in md.items():
                    miner_details[mname] = {
                        "hashrate": hr,
                        "online": miner_status.get(mname) != "offline",
                        "power": power.get(mname, 0),
                        "found_block": mname == worker
                    }
                send_alert("block_found", create_block_embed(
                    block["height"], startup_prices, startup_bri, worker,
                    miner_details=miner_details,
                    pool_block_num=state.pool_blocks_found,
                    coin_symbol=block.get("coin", primary_coin),
                    block_hash=block.get("hash"),
                    network_hashrate=net_phs_startup,
                    difficulty=net.get("difficulty") if net else None,
                ), state)
                trigger_block_celebration(miner_details)
                logger.info(f"BLOCK RECOVERED #{state.pool_blocks_found} by {sanitize_log_input(worker)} (height {block['height']}) — locked in!")
            state.save()  # Persist seen hashes immediately
        else:
            logger.info("No missed blocks while offline — all clear")

        # BUG FIX: Startup recovery must also check aux chain pools for missed blocks.
        # Without this, merge-mined blocks (NMC/SYS/XMY/FBTC on BTC, DOGE/PEP on LTC)
        # found while Sentinel was offline are missed until the first normal monitoring
        # cycle, causing delayed celebration and wrong coin_symbol/prices in the embed.
        if is_parent_chain(primary_coin):
            startup_aux_pools = discover_active_aux_pools(primary_coin)
            for aux in startup_aux_pools:
                aux_symbol = aux["symbol"]
                aux_pool_id = aux["pool_id"]
                aux_blocks = state.check_pool_for_new_blocks(
                    net_phs_startup, startup_odds,
                    coin_symbol=aux_symbol,
                    pool_id=aux_pool_id
                )
                for block in aux_blocks:
                    worker = block.get("worker") or block["miner"]
                    aux_coin = block.get("coin", aux_symbol)
                    state.pool_blocks_found += 1
                    state.record_block_time()
                    aux_prices = fetch_coin_price(aux_coin)
                    aux_bri = fetch_block_reward_for_coin(aux_coin)
                    miner_details = {}
                    for mname, hr in md.items():
                        miner_details[mname] = {
                            "hashrate": hr,
                            "online": miner_status.get(mname) != "offline",
                            "power": power.get(mname, 0),
                            "found_block": mname == worker
                        }
                    # Fetch aux chain network stats for the embed
                    _aux_net = fetch_network_stats(aux_coin)
                    send_alert("block_found", create_block_embed(
                        block["height"], aux_prices, aux_bri, worker,
                        miner_details=miner_details,
                        pool_block_num=state.pool_blocks_found,
                        coin_symbol=aux_coin,
                        block_hash=block.get("hash"),
                        network_hashrate=_aux_net.get("network_phs") if _aux_net else None,
                        difficulty=_aux_net.get("difficulty") if _aux_net else None,
                    ), state)
                    trigger_block_celebration(miner_details)
                    logger.info(f"AUX BLOCK RECOVERED #{state.pool_blocks_found} ({aux_coin}) by {sanitize_log_input(worker)} (height {block['height']}) — locked in!")
                if aux_blocks:
                    state.save()
    except Exception as e:
        logger.warning(f"Block recovery scan error (non-critical): {e}")

    # Seed orphan detection state so transitions that occurred while Sentinel was offline
    # are detected on the first main-loop cycle. check_for_orphans() records all current
    # pool blocks into known_block_statuses with their current status (no alerts fired for
    # first sightings) — only future transitions to orphaned will trigger alerts.
    try:
        state.check_for_orphans()
        logger.info("Orphan detection state seeded from current pool data")
    except Exception as e:
        logger.warning(f"Orphan seeding error (non-critical): {e}")

    # Initialize variables used by infrastructure checks before first assignment
    prices = None
    bri = None

    while True:
        try:
            now = local_now()
            in_startup_grace = (time.time() - startup_time) < STARTUP_GRACE_PERIOD

            # ═══════════════════════════════════════════════════════════════════════════════
            # HA BACKUP NODE OPTIMIZATION - Skip all polling on non-MASTER nodes
            # In Docker HA, BACKUP nodes run Sentinel but don't need to poll miners,
            # network stats, Prometheus, prices, etc. Only HA status monitoring is needed
            # to detect failover and resume full polling when promoted to MASTER.
            # ═══════════════════════════════════════════════════════════════════════════════
            if not is_master_sentinel():
                # Still monitor HA/VIP changes for failover detection
                ha_vip_change = check_ha_vip_changes()
                if ha_vip_change:
                    handle_ha_vip_alerts(ha_vip_change, state)
                # Flush any pending alert batches (e.g. queued before role change)
                if ALERT_BATCHING_ENABLED and state.should_flush_batch():
                    state.flush_alert_batch()
                logger.debug(f"[{now.strftime('%H:%M:%S')}] BACKUP node — skipping polling cycle")
                time.sleep(get_check_interval(primary_coin))
                continue

            # Check for coin changes every 15 minutes
            current_time = time.time()
            if current_time - last_coin_check >= COIN_CHECK_INTERVAL:
                last_coin_check = current_time
                coin_change = check_for_coin_change()
                if coin_change:
                    # Coin changed! Update primary_coin and notify
                    primary_coin = coin_change["new"]
                    primary_coin_name = get_coin_name(primary_coin)
                    primary_coin_emoji = get_coin_emoji(primary_coin)
                    handle_coin_change(coin_change, state)

            # Check for miner configuration changes (hot-reload from dashboard)
            # This allows adding/removing miners without restarting Sentinel
            if check_miner_reload_needed():
                reload_miners()
                # Re-push device hints when miners are reloaded
                if CONFIG.get("push_device_hints", True):
                    push_all_device_hints()

            # Periodically re-push device hints (every hour) to handle pool restarts
            if CONFIG.get("push_device_hints", True):
                if current_time - last_device_hints_push >= 3600:
                    last_device_hints_push = current_time
                    push_all_device_hints()

            # Prune stale miner state entries (hourly) to prevent unbounded memory growth
            if current_time - last_stale_cleanup >= 3600:
                last_stale_cleanup = current_time
                active_miner_names = set()
                for miner_list in MINERS.values():
                    for m in miner_list:
                        name = m.get("name") or m.get("ip", "")
                        if name:
                            active_miner_names.add(name)
                state.prune_stale_miner_state(active_miner_names)

            # Check for mode changes (solo <-> multi) every 5 minutes
            mode_change = check_for_mode_change(state)
            if mode_change:
                handle_mode_changes(mode_change, state)
                # Update coin list if mode changed
                enabled_coins = get_enabled_coins()

            # Check per-coin health (node status, sync) every 5 minutes
            if not in_startup_grace:
                coin_health = check_all_coins_health()
                handle_coin_health_alerts(coin_health, state)
                check_stuck_syncs(state)
                check_dry_streak(state)
                check_difficulty_changes(state)
                check_disk_space(state)
                check_mempool_congestion(state)
                check_backup_staleness(state)

            # Check HA/VIP changes every minute
            ha_vip_change = check_ha_vip_changes()
            if ha_vip_change:
                handle_ha_vip_alerts(ha_vip_change, state)

            # ═══════════════════════════════════════════════════════════════════════════════
            # B8: HA REPLICA COUNT DROP - Alert when HA replicas decrease
            # ═══════════════════════════════════════════════════════════════════════════════
            if not in_startup_grace and get_ha_role() != "STANDALONE":
                try:
                    ha_status = fetch_ha_status()
                    if ha_status:
                        replica_count = ha_status.get("replica_count", ha_status.get("replicas", 0))
                        if isinstance(replica_count, (int, float)) and replica_count >= 0:
                            if state.last_replica_count is not None and replica_count < state.last_replica_count:
                                alert_key = "ha_replica_drop"
                                last_alert = state.last_alerts.get(alert_key, 0)
                                cooldown = ALERT_COOLDOWNS.get("ha_replica_drop", 3600)
                                if (current_time - last_alert) >= cooldown:
                                    send_alert("ha_replica_drop", create_ha_replica_embed(state.last_replica_count, replica_count), state)
                                    state.last_alerts[alert_key] = current_time
                                    logger.warning(f"HA REPLICA DROP: {state.last_replica_count} -> {replica_count}")
                            state.last_replica_count = replica_count
                        # ─── Patroni replication lag monitoring ───
                        # Check replication lag from the HA status endpoint
                        replay_lag = ha_status.get("replay_lag", ha_status.get("replication_lag_bytes", 0))
                        HA_REPLICATION_LAG_THRESHOLD = CONFIG.get("ha_replication_lag_threshold", 10 * 1024 * 1024)  # 10MB default
                        if isinstance(replay_lag, (int, float)) and replay_lag > HA_REPLICATION_LAG_THRESHOLD:
                            alert_key = "ha_replication_lag"
                            last_alert = state.last_alerts.get(alert_key, 0)
                            cooldown = ALERT_COOLDOWNS.get("ha_replication_lag", 3600)
                            if (current_time - last_alert) >= cooldown:
                                # Estimate catch-up time: lag_bytes / estimated WAL replay rate (~50MB/s typical)
                                wal_replay_rate = CONFIG.get("ha_wal_replay_rate", 50 * 1024 * 1024)  # 50MB/s default
                                lag_seconds = replay_lag / wal_replay_rate if wal_replay_rate > 0 else 0
                                send_alert("ha_replication_lag",
                                           create_ha_replication_lag_embed(replay_lag, lag_seconds, HA_REPLICATION_LAG_THRESHOLD),
                                           state)
                                state.last_alerts[alert_key] = current_time
                                logger.warning(f"HA REPLICATION LAG: {replay_lag / (1024*1024):.1f}MB (threshold: {HA_REPLICATION_LAG_THRESHOLD / (1024*1024):.0f}MB)")

                except Exception as e:
                    logger.debug(f"HA replica check error: {e}")

            # ═══════════════════════════════════════════════════════════════════════════════
            # B9: POST-FAILOVER RESYNC DETECTION — alert when failover completes
            # ═══════════════════════════════════════════════════════════════════════════════
            if not in_startup_grace and get_ha_role() != "STANDALONE":
                try:
                    # Detect failover → running transition
                    prev_ha_state = getattr(state, '_prev_ha_state_for_resync', None)
                    current_ha_state = _last_ha_state
                    if prev_ha_state in ("failover",) and current_ha_state == "running":
                        alert_key = "ha_resync"
                        last_alert = state.last_alerts.get(alert_key, 0)
                        cooldown = ALERT_COOLDOWNS.get("ha_resync", 1800)
                        if (current_time - last_alert) >= cooldown:
                            details = ("• **Database:** Patroni handles replication automatically via streaming replication (typically minutes)\n"
                                       "• **Blockchain:** P2P network sync — short outage (<1h) resyncs quickly\n"
                                       "• Extended outage may need manual `ha-replicate.sh`")
                            send_alert("ha_resync",
                                       create_ha_resync_embed("Post-Failover", 0, 0, details=details),
                                       state)
                            state.last_alerts[alert_key] = current_time
                            logger.info("HA POST-FAILOVER RESYNC: Failover completed, resync alert sent")
                    state._prev_ha_state_for_resync = current_ha_state
                except Exception as e:
                    logger.debug(f"HA resync check error: {e}")

            # Check infrastructure health (Prometheus metrics) periodically
            if CONFIG.get("metrics_enabled", True) and (current_time - last_infra_check) >= INFRA_CHECK_INTERVAL:
                last_infra_check = current_time
                infra_alerts = update_infrastructure_health()
                if infra_alerts and not in_startup_grace:
                    for alert_type, message, severity in infra_alerts:
                        # Check if this alert type is enabled
                        alert_config_key = f"infra_{alert_type}_alert"
                        if CONFIG.get(alert_config_key, True):
                            embed = create_infrastructure_alert_embed(alert_type, message, severity)
                            # Use unique alert key for cooldown tracking
                            alert_key = f"infra_{alert_type}"
                            last_alert = state.last_alerts.get(alert_key, 0)
                            # 5 min cooldown for info alerts, 30 min for warnings, no cooldown for critical
                            cooldown = 0 if severity == "critical" else (300 if severity == "info" else 1800)
                            if (current_time - last_alert) >= cooldown:
                                send_alert(alert_key, embed, state)
                                state.last_alerts[alert_key] = current_time
                                logger.info(f"Infrastructure alert: {alert_type} - {message} ({severity})")

                # ═══════════════════════════════════════════════════════════════════════════
                # B3: ORPHAN RATE SPIKE - Detect new orphaned blocks from Prometheus
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    current_orphans = _infra_health.get_blocks_orphaned()
                    if current_orphans > state.last_known_orphan_count and state.orphan_count_initialized:
                        new_orphans = current_orphans - state.last_known_orphan_count
                        alert_key = "orphan_rate_spike"
                        last_alert = state.last_alerts.get(alert_key, 0)
                        cooldown = ALERT_COOLDOWNS.get("orphan_rate_spike", 3600)
                        if (current_time - last_alert) >= cooldown:
                            # Calculate revenue loss for the embed
                            orphan_reward = None
                            if bri:
                                orphan_reward = bri.get("sha256_reward", bri.get("scrypt_reward", bri.get("block_reward", 0)))
                            send_alert("orphan_rate_spike", create_orphan_spike_embed(new_orphans, current_orphans, coin=primary_coin, block_reward=orphan_reward, prices=prices), state)
                            state.last_alerts[alert_key] = current_time
                            logger.warning(f"ORPHAN SPIKE: {new_orphans} new orphan(s), total: {current_orphans}")
                    state.last_known_orphan_count = current_orphans
                    state.orphan_count_initialized = True
                except Exception as e:
                    logger.debug(f"Orphan rate check error: {e}")

                # ═══════════════════════════════════════════════════════════════════════════
                # B4: ZMQ MESSAGE STALENESS - Alert when ZMQ notifications stop arriving
                # Threshold is per-coin: fast-block coins (DGB 90s) alert much sooner than BTC (1800s)
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    zmq_age = _infra_health.get_zmq_last_message_age()
                    zmq_threshold = get_zmq_stale_threshold(primary_coin)
                    if zmq_age and zmq_age > zmq_threshold:
                        if not state.zmq_stale_alerted:
                            alert_key = "zmq_stale"
                            last_alert = state.last_alerts.get(alert_key, 0)
                            cooldown = ALERT_COOLDOWNS.get("zmq_stale", 1800)
                            if (current_time - last_alert) >= cooldown:
                                send_alert("zmq_stale", create_zmq_stale_embed(zmq_age, zmq_threshold, primary_coin), state)
                                state.last_alerts[alert_key] = current_time
                                state.zmq_stale_alerted = True
                                logger.warning(f"ZMQ STALE: Last message {zmq_age:.0f}s ago (threshold {zmq_threshold}s for {primary_coin})")
                    elif zmq_age is not None and zmq_age <= zmq_threshold:
                        state.zmq_stale_alerted = False
                except Exception as e:
                    logger.debug(f"ZMQ stale check error: {e}")

                # ═══════════════════════════════════════════════════════════════════════════
                # B5: WORKER/CONNECTION COUNT DROP - Alert on significant worker decrease
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    current_workers = _infra_health.get_active_workers()
                    if current_workers is not None and current_workers >= 0:
                        # Only update baseline when workers are in normal range (prevents baseline absorption)
                        if len(state.worker_count_baseline) >= 3:
                            current_avg = sum(state.worker_count_baseline) / len(state.worker_count_baseline)
                            if current_workers >= current_avg * 0.5:
                                state.worker_count_baseline.append(current_workers)
                                state.worker_count_baseline = state.worker_count_baseline[-10:]
                        else:
                            state.worker_count_baseline.append(current_workers)
                            state.worker_count_baseline = state.worker_count_baseline[-10:]
                        if len(state.worker_count_baseline) >= 3:
                            baseline_avg = sum(state.worker_count_baseline[:-1]) / len(state.worker_count_baseline[:-1])
                            if baseline_avg >= 3 and current_workers < baseline_avg * 0.5:
                                alert_key = "worker_count_drop"
                                last_alert = state.last_alerts.get(alert_key, 0)
                                cooldown = ALERT_COOLDOWNS.get("worker_count_drop", 1800)
                                if (current_time - last_alert) >= cooldown:
                                    send_alert("worker_count_drop", create_worker_drop_embed(current_workers, baseline_avg), state)
                                    state.last_alerts[alert_key] = current_time
                                    logger.warning(f"WORKER DROP: {current_workers} vs baseline {baseline_avg:.0f}")
                except Exception as e:
                    logger.debug(f"Worker count check error: {e}")

                # ═══════════════════════════════════════════════════════════════════════════
                # B6: SHARE LOSS RATE - Alert when share batch loss exceeds threshold
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    loss_rate = _infra_health.get_share_loss_rate()
                    if loss_rate is not None and loss_rate > 0.001:  # 0.1%
                        if not state.share_loss_alerted:
                            alert_key = "share_loss_rate"
                            last_alert = state.last_alerts.get(alert_key, 0)
                            cooldown = ALERT_COOLDOWNS.get("share_loss_rate", 1800)
                            if (current_time - last_alert) >= cooldown:
                                send_alert("share_loss_rate", create_share_loss_embed(loss_rate * 100), state)
                                state.last_alerts[alert_key] = current_time
                                state.share_loss_alerted = True
                                logger.warning(f"SHARE LOSS: {loss_rate*100:.3f}%")
                    elif loss_rate is not None and loss_rate <= 0.001:
                        state.share_loss_alerted = False
                except Exception as e:
                    logger.debug(f"Share loss check error: {e}")

                # ═══════════════════════════════════════════════════════════════════════════
                # B7: BLOCK NOTIFY MODE CHANGE - Detect ZMQ <-> polling fallback
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    notify_mode = _infra_health.get_block_notify_mode()
                    if notify_mode is not None:
                        if state.last_block_notify_mode is not None and notify_mode != state.last_block_notify_mode:
                            alert_key = "block_notify_mode_change"
                            last_alert = state.last_alerts.get(alert_key, 0)
                            cooldown = ALERT_COOLDOWNS.get("block_notify_mode_change", 3600)
                            if (current_time - last_alert) >= cooldown:
                                send_alert("block_notify_mode_change", create_notify_mode_embed(state.last_block_notify_mode, notify_mode), state)
                                state.last_alerts[alert_key] = current_time
                                logger.info(f"BLOCK NOTIFY MODE: {state.last_block_notify_mode} -> {notify_mode}")
                        state.last_block_notify_mode = notify_mode
                except Exception as e:
                    logger.debug(f"Block notify mode check error: {e}")

                # ═══════════════════════════════════════════════════════════════════════════
                # CIRCUIT BREAKER - Pool actively dropping shares
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    cb_state = int(_infra_health.metrics.get("stratum_circuit_breaker_state", 0))
                    if cb_state != 0:
                        if not state.circuit_breaker_alerted:
                            send_alert("circuit_breaker", create_circuit_breaker_embed(cb_state), state)
                            state.circuit_breaker_alerted = True
                            logger.warning(f"CIRCUIT BREAKER: State={cb_state} - pool rejecting shares!")
                    else:
                        state.circuit_breaker_alerted = False
                except Exception as e:
                    logger.debug(f"Circuit breaker check error: {e}")

                # ═══════════════════════════════════════════════════════════════════════════
                # BACKPRESSURE - Pool buffer overflow
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    bp_level = int(_infra_health.metrics.get("stratum_backpressure_level", 0))
                    bp_fill = float(_infra_health.metrics.get("stratum_backpressure_buffer_fill_percent", 0))
                    if bp_level >= 2:  # CRITICAL or EMERGENCY
                        if not state.backpressure_alerted:
                            alert_key = "backpressure"
                            last_alert = state.last_alerts.get(alert_key, 0)
                            cooldown = ALERT_COOLDOWNS.get("backpressure", 300)
                            if (current_time - last_alert) >= cooldown:
                                send_alert("backpressure", create_backpressure_embed(bp_level, bp_fill), state)
                                state.last_alerts[alert_key] = current_time
                                state.backpressure_alerted = True
                                logger.warning(f"BACKPRESSURE: Level={bp_level} Fill={bp_fill:.0f}%")
                    elif bp_level < 2:
                        state.backpressure_alerted = False
                except Exception as e:
                    logger.debug(f"Backpressure check error: {e}")

                # ═══════════════════════════════════════════════════════════════════════════
                # WAL DATABASE ERRORS - Silent share data loss
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    wal_writes = int(_infra_health.metrics.get("stratum_wal_write_errors_total", 0))
                    wal_commits = int(_infra_health.metrics.get("stratum_wal_commit_errors_total", 0))
                    delta_writes = wal_writes - state.last_wal_write_errors
                    delta_commits = wal_commits - state.last_wal_commit_errors
                    if (delta_writes > 0 or delta_commits > 0) and state.wal_errors_initialized:
                        send_alert("wal_errors", create_wal_errors_embed(wal_writes, wal_commits, delta_writes, delta_commits), state)
                        logger.warning(f"WAL ERRORS: +{delta_writes} write, +{delta_commits} commit (total: {wal_writes}/{wal_commits})")
                    state.last_wal_write_errors = wal_writes
                    state.last_wal_commit_errors = wal_commits
                    state.wal_errors_initialized = True
                except Exception as e:
                    logger.debug(f"WAL error check error: {e}")

                # ═══════════════════════════════════════════════════════════════════════════
                # ZMQ SOCKET HEALTH - Direct connection state (no Poisson constraint)
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    zmq_health = int(_infra_health.metrics.get("stratum_zmq_health_status", 2))
                    zmq_connected = int(_infra_health.metrics.get("stratum_zmq_connected", 1))
                    # Alert when ZMQ is degraded/failed OR disconnected
                    if zmq_health >= 3 or zmq_connected == 0:
                        if not state.zmq_disconnected_alerted:
                            alert_key = "zmq_disconnected"
                            last_alert = state.last_alerts.get(alert_key, 0)
                            cooldown = ALERT_COOLDOWNS.get("zmq_disconnected", 1800)
                            if (current_time - last_alert) >= cooldown:
                                send_alert("zmq_disconnected", create_zmq_disconnected_embed(zmq_health, zmq_connected), state)
                                state.last_alerts[alert_key] = current_time
                                state.zmq_disconnected_alerted = True
                                logger.warning(f"ZMQ DISCONNECTED: health={zmq_health} connected={zmq_connected}")
                    elif zmq_health <= 2 and zmq_connected == 1:
                        state.zmq_disconnected_alerted = False
                except Exception as e:
                    logger.debug(f"ZMQ socket health check error: {e}")

                # ═══════════════════════════════════════════════════════════════════════════
                # BEST SHARE DIFFICULTY MILESTONE - Celebratory for solo miners
                # ═══════════════════════════════════════════════════════════════════════════
                try:
                    current_best = float(_infra_health.metrics.get("stratum_best_share_difficulty", 0))
                    if current_best > 0 and current_best > state.best_share_difficulty:
                        previous = state.best_share_difficulty
                        state.best_share_difficulty = current_best
                        # Only alert if there was a previous record to beat (skip first reading)
                        if previous > 0:
                            send_alert("best_share", create_best_share_embed(current_best, previous), state)
                            logger.info(f"BEST SHARE: New record {current_best} (was {previous})")
                except Exception as e:
                    logger.debug(f"Best share check error: {e}")

            # ═══════════════════════════════════════════════════════════════════════════════
            # API SENTINEL ALERT BRIDGE - Poll Go pool for internal health alerts
            # Runs on same interval as infrastructure checks. Bridges Go-only alerts
            # (WAL, share DB, payment, daemon peers, etc.) to Discord/Telegram.
            # ═══════════════════════════════════════════════════════════════════════════════
            if CONFIG.get("api_sentinel_enabled", True) and not in_startup_grace:
                if (current_time - _api_sentinel_last_poll) >= INFRA_CHECK_INTERVAL:
                    try:
                        check_api_sentinel_alerts(state)
                    except Exception as e:
                        logger.debug(f"API Sentinel poll error: {e}")

            # ═══════════════════════════════════════════════════════════════════════════════
            # SATS SURGE CHECK - Track and alert on coin/BTC value increases
            # ═══════════════════════════════════════════════════════════════════════════════
            # Records sat values periodically and alerts when any coin's sat value
            # increases by 25%+ over 1 week baseline. Helps identify good swap times.
            if SATS_SURGE_ENABLED and (current_time - last_sats_sample) >= SATS_SAMPLE_INTERVAL:
                last_sats_sample = current_time
                all_prices = fetch_all_prices()
                if all_prices:
                    # Record sat values for all tracked coins
                    state.record_sats_sample(all_prices)

                    # Record fiat prices for crash detection
                    if PRICE_CRASH_ENABLED:
                        state.record_price_sample(all_prices)

                    # Check for surges (only after we have enough history)
                    if not in_startup_grace:
                        surges = state.check_sats_surge()
                        for surge in surges:
                            embed = create_sats_surge_embed(surge, all_prices=all_prices)
                            send_alert("sats_surge", embed, state)
                            logger.info(f"SATS SURGE: {surge['coin']} up {surge['change_pct']:.1f}% vs {surge['lookback_days']}d ago ({surge['baseline_sats']} -> {surge['current_sats']} sats)")

                        # Check for price crashes
                        if PRICE_CRASH_ENABLED:
                            crashes = state.check_price_crash()
                            for crash in crashes:
                                embed = create_price_crash_embed(crash, all_prices)
                                send_alert("price_crash", embed, state)
                                logger.warning(f"PRICE CRASH: {crash['coin']} down {crash['drop_pct']:.1f}% (${crash['baseline_usd']:.6f} -> ${crash['current_usd']:.6f})")

            net = fetch_network_stats(primary_coin)
            fleet_ths, md, temps, miner_status, power, uptimes, mblocks, mpools, mstats, worker_names, fans, chain_data, hw_errors_data = get_total_hashrate()
            # Use coin-specific price and block reward fetching
            prices = fetch_coin_price(primary_coin)
            bri = fetch_block_reward_for_coin(primary_coin)

            if not net:
                logger.warning("Network unreachable...")
                # ── Stratum-down detection ──────────────────────────────────────
                global _stratum_down_since, _stratum_down_alerted
                if _stratum_down_since is None:
                    _stratum_down_since = current_time
                    _stratum_down_alerted = False
                    logger.warning("Pool API became unreachable — starting outage timer")
                elif not _stratum_down_alerted and not in_startup_grace:
                    outage_secs = current_time - _stratum_down_since
                    if outage_secs >= 300:  # 5-minute grace before alerting
                        send_notifications(create_stratum_down_embed(_stratum_down_since))
                        _stratum_down_alerted = True
                        logger.warning(f"Stratum-down alert sent (outage {outage_secs:.0f}s)")
                # P1 AUDIT FIX: Use coin-aware interval even during errors
                time.sleep(get_check_interval(primary_coin))
                continue

            # ── Stratum recovery ────────────────────────────────────────────────
            if _stratum_down_since is not None:
                if _stratum_down_alerted:
                    send_notifications(create_stratum_recovered_embed(_stratum_down_since, current_time))
                    logger.info(f"Stratum recovered after {current_time - _stratum_down_since:.0f}s outage")
                _stratum_down_since = None
                _stratum_down_alerted = False

            net_phs, diff = net["network_phs"], net.get("difficulty")
            odds = calc_odds(net_phs, fleet_ths, primary_coin)
            state.record_sample(net_phs, fleet_ths, odds)
            extremes = state.get_extremes()

            # ═══════════════════════════════════════════════════════════════════════════════
            # PAYOUT TRACKING - Detect wallet balance changes (payouts received/missing)
            # ═══════════════════════════════════════════════════════════════════════════════
            if not in_startup_grace and (current_time - last_payout_check) >= PAYOUT_CHECK_INTERVAL:
                last_payout_check = current_time
                try:
                    coin_config = get_primary_coin_config()
                    wallet_addr = coin_config.get("wallet_address", CONFIG.get("wallet_address", ""))
                    if wallet_addr and "YOUR" not in wallet_addr:
                        current_balance = fetch_wallet_balance_for_coin(wallet_addr, primary_coin)
                        if current_balance is not None:
                            prev_balance = state.last_wallet_balance

                            payout_deferred = False

                            if prev_balance is not None:
                                balance_change = current_balance - prev_balance

                                # Payout received (balance increased)
                                if balance_change > 0:
                                    was_deferred = state.payout_deferred_from_quiet
                                    embed = create_payout_received_embed(primary_coin, balance_change, current_balance, prices, deferred=was_deferred)
                                    alert_sent = send_alert("payout_received", embed, state)
                                    if alert_sent:
                                        state.missing_payout_alerted = False  # Reset missing payout tracker
                                        state.wallet_balance_last_check = current_time  # Reset activity timer
                                        state.payout_deferred_from_quiet = False  # Clear deferral flag
                                        logger.info(f"PAYOUT RECEIVED: {balance_change} {primary_coin} (new balance: {current_balance}){' [was deferred from quiet hours]' if was_deferred else ''}")
                                    else:
                                        # Alert suppressed (quiet hours) — do NOT update last_wallet_balance
                                        # so the payout alert retries on the next check after quiet hours end
                                        payout_deferred = True
                                        state.payout_deferred_from_quiet = True
                                        logger.info(f"PAYOUT RECEIVED (alert deferred): {balance_change} {primary_coin} — payout completed but alert suppressed due to quiet hours, will retry after quiet hours end")

                                # Missing payout check (no change for N days)
                                elif balance_change == 0:
                                    if not state.missing_payout_alerted:
                                        days_since_check_start = (current_time - state.wallet_balance_last_check) / 86400
                                        if state.wallet_balance_last_check > 0 and days_since_check_start >= MISSING_PAYOUT_DAYS:
                                            alert_key = "missing_payout"
                                            last_alert = state.last_alerts.get(alert_key, 0)
                                            cooldown = ALERT_COOLDOWNS.get("missing_payout", 86400)
                                            if (current_time - last_alert) >= cooldown:
                                                embed = create_missing_payout_embed(primary_coin, int(days_since_check_start), current_balance)
                                                if send_alert("missing_payout", embed, state):
                                                    state.last_alerts[alert_key] = current_time
                                                    state.missing_payout_alerted = True
                                                    logger.warning(f"MISSING PAYOUT: No balance change in {int(days_since_check_start)} days")
                                    # If already alerted, do nothing — wait for actual balance change

                                else:
                                    # Balance decreased - alert if wallet drop detection enabled
                                    amount_dropped = abs(balance_change)
                                    if CONFIG.get("wallet_drop_alert_enabled", True) and amount_dropped > 0.001:
                                        embed = create_wallet_drop_embed(primary_coin, amount_dropped, prev_balance, current_balance, prices)
                                        send_alert("wallet_drop", embed, state)
                                        logger.warning(f"WALLET DROP: {amount_dropped} {primary_coin} lost (was {prev_balance}, now {current_balance})")
                                    state.wallet_balance_last_check = current_time
                                    state.missing_payout_alerted = False

                            else:
                                # First reading - initialize tracking
                                state.wallet_balance_last_check = current_time

                            if not payout_deferred:
                                state.last_wallet_balance = current_balance
                except Exception as e:
                    logger.debug(f"Payout tracking error: {e}")

            # ═══════════════════════════════════════════════════════════════════════════════
            # REVENUE VELOCITY - Alert when current month's earnings pace is declining
            # ═══════════════════════════════════════════════════════════════════════════════
            if not in_startup_grace and (current_time - last_revenue_check) >= REVENUE_CHECK_INTERVAL:
                last_revenue_check = current_time
                try:
                    velocity = state.check_revenue_velocity()
                    if velocity:
                        embed = create_revenue_decline_embed(
                            velocity["current_pace"],
                            velocity["previous_month"],
                            velocity["decline_pct"],
                            velocity["days_elapsed"]
                        )
                        send_alert("revenue_decline", embed, state)
                        cur = get_currency_meta()
                        logger.warning(f"REVENUE DECLINE: Pace down {velocity['decline_pct']:.0f}% vs last month ({cur['symbol']}{velocity['current_pace']:,.2f} vs {cur['symbol']}{velocity['previous_month']:,.2f} at day {velocity['days_elapsed']})")
                except Exception as e:
                    logger.debug(f"Revenue velocity check error: {e}")

            # Track hashrate for 24h trend arrows
            state.record_hashrate_sample(fleet_ths)

            # Update personal bests
            all_online = all(st != "offline" for st in miner_status.values()) if miner_status else False
            state.update_personal_bests(fleet_ths, odds.get("daily_odds_pct", 0), all_online)
            state.update_uptime_streak(all_online, len(miner_status))

            # Record 15-minute samples for historical trend tracking (per-coin)
            if history.record_sample(diff, net_phs, fleet_ths, coin=primary_coin):
                history.save()  # Persist to disk after each new sample

            # Record aux chain difficulty samples (merge mining)
            # When mining a parent chain (BTC/LTC), aux chains share the same hashrate
            # but have their own independent difficulty that needs separate tracking.
            # Pass fleet_ths=0 to avoid duplicate fleet entries (already recorded above).
            if is_parent_chain(primary_coin):
                aux_saved = False
                for aux_symbol in get_enabled_aux_chains(primary_coin):
                    if history.should_sample(aux_symbol):
                        try:
                            aux_net = fetch_network_stats(aux_symbol)
                            if aux_net:
                                aux_diff = aux_net.get("difficulty")
                                aux_phs = aux_net.get("network_phs", 0)
                                if history.record_sample(aux_diff, aux_phs, 0, coin=aux_symbol):
                                    aux_saved = True
                                    logger.debug(f"Recorded aux chain sample: {aux_symbol} diff={format_difficulty(aux_diff) if aux_diff else 'N/A'}")
                        except Exception as e:
                            logger.debug(f"Failed to record aux chain sample for {aux_symbol}: {e}")
                if aux_saved:
                    history.save()

            # Get trend data for reports (per-coin for difficulty/network, shared for fleet)
            diff_trends = history.get_multi_period_trends("difficulty", coin=primary_coin)
            net_trends = history.get_multi_period_trends("network_phs", coin=primary_coin)
            fleet_trends = history.get_multi_period_trends("fleet_ths")  # Shared across coins

            # Get aux chain difficulty trends for merge mining reports
            aux_diff_trends = {}
            if is_parent_chain(primary_coin):
                for aux_symbol in get_enabled_aux_chains(primary_coin):
                    aux_trends = history.get_multi_period_trends("difficulty", coin=aux_symbol)
                    if aux_trends:
                        aux_diff_trends[aux_symbol] = aux_trends

            # Record miner samples & health
            health_scores = {}
            miner_hashrates = {}
            for name, st in miner_status.items():
                state.record_miner_status(name, st not in ("offline", "pool_only"))
                if st not in ("offline", "pool_only"):
                    # ESP32 miners are lottery miners (kH/s) — they go hours/days without shares.
                    # Skip share tracking, zombie detection, rejection spikes, and hashrate baselines
                    # to avoid false alerts. They're still tracked for online/offline status above.
                    is_esp32 = _miner_type_lookup.get(name) == "esp32miner"

                    # Parse hashrate from display string, handling all unit formats (TH/s, GH/s, MH/s, etc.)
                    hr_str = md.get(name, "0")
                    hr = 0
                    try:
                        hr_val = float(hr_str.split()[0])
                        # Convert to GH/s based on unit
                        if "EH/s" in hr_str:
                            hr = hr_val * 1e9  # EH to GH
                        elif "PH/s" in hr_str:
                            hr = hr_val * 1e6  # PH to GH
                        elif "TH/s" in hr_str:
                            hr = hr_val * 1e3  # TH to GH
                        elif "GH/s" in hr_str:
                            hr = hr_val  # Already GH
                        elif "MH/s" in hr_str:
                            hr = hr_val / 1e3  # MH to GH
                        elif "KH/s" in hr_str:
                            hr = hr_val / 1e6  # KH to GH
                        elif "H/s" in hr_str:
                            hr = hr_val / 1e9  # H to GH
                    except (ValueError, IndexError):
                        hr = 0
                    miner_hashrates[name] = hr
                    temp = temps.get(name, {}).get("chip", temps.get(name, {}).get("board"))
                    state.record_miner_sample(name, hr, temp)
                    
                    # Track stales for zombie detection (skip ESP32 — too slow for meaningful share deltas)
                    if not is_esp32:
                        stale = mstats.get(name, {})
                        if stale:
                            state.track_miner_stales(name, stale.get("accepted", 0), stale.get("rejected", 0), stale.get("stale", 0))

                    # ── B2: SHARE REJECTION RATE SPIKE ──
                    # Check last 15 samples (~30 min) for rejection rate > 20% with minimum 100 shares
                    # Short windows (5 samples) cause false spikes — a brief burst of 12 rejects
                    # in 105 shares = 11.4%, while lifetime rate is only 5%. Using 15 samples
                    # smooths out transient bursts and only alerts on sustained rejection issues.
                    # Exclude stale shares from rejection count — stale work is normal mining
                    # behavior (cgminer/Avalon counts stale as subset of rejected, inflating %)
                    # Threshold raised from 15% to 20% — Avalon chips have transient spikes during
                    # job transitions that are normal hardware behavior, not real pool-side issues.
                    if name in state.miner_stale_history and len(state.miner_stale_history[name]) >= 15:
                        recent = [s for s in state.miner_stale_history[name][-15:] if not s.get("is_baseline")]
                        total_acc = sum(s.get("accepted", 0) for s in recent)
                        total_rej = sum(s.get("rejected", 0) for s in recent)
                        total_stale = sum(s.get("stale", 0) for s in recent)
                        # True rejections = rejected minus stale (cgminer counts stale within rejected)
                        true_rej = max(0, total_rej - total_stale)
                        total_shares = total_acc + total_rej
                        if total_shares >= 100 and true_rej > 0:
                            reject_pct = (true_rej / total_shares) * 100
                            if reject_pct > 20:
                                rej_alert_key = f"share_rejection_spike:{name}"
                                last_rej_alert = state.last_alerts.get(rej_alert_key, 0)
                                rej_cooldown = ALERT_COOLDOWNS.get("share_rejection_spike", 3600)
                                if (current_time - last_rej_alert) >= rej_cooldown:
                                    stale_pct = (total_stale / total_shares) * 100 if total_shares > 0 else 0
                                    send_alert("share_rejection_spike", create_rejection_spike_embed(name, reject_pct, total_acc, true_rej, total_stale, stale_pct), state, miner_name=name)
                                    state.last_alerts[rej_alert_key] = current_time
                                    logger.warning(f"REJECTION SPIKE: {sanitize_log_input(name)} {reject_pct:.1f}% reject + {stale_pct:.1f}% stale ({true_rej}+{total_stale}/{total_shares})")
                                    # Kick the stratum session — forces a clean reconnect and difficulty
                                    # re-negotiation, which resolves most rejection spikes without a reboot
                                    rej_miner_ip = next(
                                        (m.get("ip") for mt in ALL_MINER_TYPES for m in MINERS.get(mt, []) if m["name"] == name),
                                        None
                                    )
                                    if rej_miner_ip:
                                        kicked = kick_stratum_session(rej_miner_ip)
                                        if kicked:
                                            logger.info(f"REJECTION SPIKE: kicked {kicked} stratum session(s) for {sanitize_log_input(name)} @ {rej_miner_ip}")

                    # Track pool-side share verification (if enabled)
                    if CONFIG.get("pool_share_validation", True):
                        pool_stats, has_per_worker = get_pool_share_stats()
                        # Only track individual miners if pool supports per-worker breakdown
                        # If pool only returns wallet-level aggregate, skip to avoid false zombie alerts
                        if has_per_worker:
                            # Try multiple identifiers for pool matching: worker_name -> hostname -> IP -> display name
                            miner_ids = worker_names.get(name, {})
                            pool_worker = None
                            for candidate in [miner_ids.get("worker"), miner_ids.get("hostname"), miner_ids.get("ip"), name]:
                                if candidate and candidate in pool_stats:
                                    pool_worker = candidate
                                    break
                            if pool_worker:
                                ps = pool_stats[pool_worker]
                                state.track_pool_shares(name, ps.get("hashrate", 0), ps.get("shares_per_second", 0))
                            # else: Miner not found by any identifier - skip tracking to avoid false zombie alerts
                            # (Worker name mismatch or pool doesn't have this worker in response)
                        # else: pool only has wallet-level data, don't track individual miners

                    # All supported miner types for restart and health checks
                    ALL_MINER_TYPES = ["nerdqaxe", "nmaxe", "axeos", "avalon", "antminer", "antminer_scrypt", "whatsminer", "innosilicon", "futurebit", "hammer", "goldshell", "luckyminer", "jingleminer", "zyber", "esp32miner"]

                    # Zombie detection (only if health monitoring enabled, skip ESP32 lottery miners)
                    # Remediation strategy: kick stratum session first (fast, ~5s recovery);
                    # only escalate to a full miner reboot if the zombie condition persists
                    # 15+ minutes after the kick was attempted.
                    if HEALTH_MONITORING_ENABLED and not is_esp32:
                        zombie = state.check_zombie_miner(name)
                        if zombie and (time.time() - state.get_last_restart_time(name)) > 900 and uptimes.get(name, 0) > 900 and not in_startup_grace:
                            logger.warning(f"ZOMBIE: {sanitize_log_input(name)} - {sanitize_log_input(zombie['reason'])}")
                            send_alert("zombie_miner", create_zombie_embed(name, zombie), state, miner_name=name)
                            state.track_chronic_issue(name, "zombie_miner")

                            last_kick = state.zombie_kick_times.get(name, 0)
                            kick_age = time.time() - last_kick
                            # ZOMBIE_KICK_WINDOW: how long to wait after a kick before escalating to reboot
                            ZOMBIE_KICK_WINDOW = 900  # 15 minutes — enough time for miner to reconnect and reestablish shares

                            # Find miner IP for stratum kick
                            miner_ip = None
                            for miner_type in ALL_MINER_TYPES:
                                for m in MINERS.get(miner_type, []):
                                    if m["name"] == name:
                                        miner_ip = m.get("ip")
                                        break
                                if miner_ip:
                                    break

                            if last_kick == 0:
                                # First detection — kick stratum session first; reboot only if it
                                # doesn't resolve within ZOMBIE_KICK_WINDOW (15 min)
                                kicked = kick_stratum_session(miner_ip) if miner_ip else 0
                                state.zombie_kick_times[name] = time.time()
                                if kicked:
                                    logger.info(f"ZOMBIE: kicked {kicked} stratum session(s) for {sanitize_log_input(name)} @ {miner_ip} — awaiting reconnect")
                                else:
                                    logger.info(f"ZOMBIE: stratum kick attempted for {sanitize_log_input(name)} (no active sessions or admin key not configured)")
                            elif kick_age < ZOMBIE_KICK_WINDOW:
                                # Kick was attempted recently — wait for miner to recover before escalating
                                logger.debug(f"ZOMBIE: {sanitize_log_input(name)} — kick attempted {kick_age/60:.1f}m ago, waiting for reconnect ({ZOMBIE_KICK_WINDOW//60}m threshold)")
                            else:
                                # Kick attempted but zombie condition persists beyond the window — escalate
                                logger.warning(f"ZOMBIE: kick did not resolve issue for {sanitize_log_input(name)} after {kick_age/60:.0f} min — escalating to miner reboot")
                                state.zombie_kick_times.pop(name, None)  # Reset so next detection starts with a kick again
                                # Note: Don't record_miner_restart here - blip detection will record it
                                # when the miner's uptime actually resets, avoiding false restart counts
                                for miner_type in ALL_MINER_TYPES:
                                    for m in MINERS.get(miner_type, []):
                                        if m["name"] == name:
                                            restart_miner(miner_type, m["ip"], m.get("port", 4028))
                                            break

                    # Get expected hashrate from any miner type
                    all_miners = []
                    for mt in ALL_MINER_TYPES:
                        all_miners.extend(MINERS.get(mt, []))
                    exp = next((m.get("fallback_ths", 0)*1000 or m.get("fallback_ghs", 0) for m in all_miners if m["name"]==name), 0)
                    score, _ = state.calc_health_score(name, state.get_uptime(name), exp)
                    health_scores[name] = score

            # Block detection via miner-reported found_blocks
            # NOTE: This only works for industrial ASICs (Antminer, Whatsminer, Innosilicon)
            # BitAxe/AxeOS devices have has_blocks=False because their "found_blocks" counter
            # increments on personal best difficulty shares, not real network blocks.
            # Track which workers and block hashes we've alerted for in this cycle to avoid
            # double alerts. Worker-name dedup covers most cases; hash-based dedup covers
            # the case where CGMiner reports a different worker name than the pool API source
            # field (e.g. CGMiner says "q" but pool API source says "BG-03").
            alerted_workers_this_cycle = set()
            alerted_hashes_this_cycle = set()
            if mblocks:
                finders = state.check_new_blocks(mblocks, net_phs, odds, primary_coin)
                for f in finders:
                    state.pool_blocks_found += f.get("new", 1)
                    # Track fastest block time for personal bests
                    state.record_block_time()
                    # Build miner details for the block embed
                    miner_details = {}
                    for mname, hr in md.items():
                        miner_details[mname] = {
                            "hashrate": hr,
                            "online": miner_status.get(mname) != "offline",
                            "power": power.get(mname, 0),
                            "found_block": mname == f["miner"]
                        }
                    # Calculate time since last block and effort for the embed
                    _time_since = None
                    _effort = None
                    if len(state.block_history) >= 2:
                        _time_since = state.block_history[-1]["t"] - state.block_history[-2]["t"]
                    elif len(state.block_history) == 1 and hasattr(state, '_first_block_time'):
                        _time_since = state.block_history[-1]["t"] - state._first_block_time
                    if _time_since and odds.get("expected_days"):
                        expected_secs = odds["expected_days"] * 86400
                        _effort = (_time_since / expected_secs * 100) if expected_secs > 0 else None
                    send_alert("block_found", create_block_embed(
                        None, prices, bri, f["miner"],
                        miner_details=miner_details,
                        pool_block_num=state.pool_blocks_found,
                        coin_symbol=primary_coin,
                        time_since_last=_time_since,
                        effort_pct=_effort,
                        block_hash=f.get("hash"),
                        network_hashrate=net_phs,
                        difficulty=diff,
                        observed_hashrate_hs=fleet_ths * 1e12,
                    ), state)
                    trigger_block_celebration(miner_details)
                    logger.info(f"BLOCK #{state.pool_blocks_found} by {sanitize_log_input(f['miner'])}!")
                    alerted_workers_this_cycle.add(f["miner"])
                    if f.get("hash"):
                        alerted_hashes_this_cycle.add(f["hash"])

            # ═══════════════════════════════════════════════════════════════════════════════
            # POOL-SIDE BLOCK DETECTION - Works for ALL miner types
            # ═══════════════════════════════════════════════════════════════════════════════
            # This detects blocks via pool API, which works for ALL miners including:
            # nmaxe, nerdqaxe, avalon, hammer, axeos (which don't reliably report found_blocks)
            # Skip workers already alerted via miner-reported detection to avoid duplicates.
            pool_new_blocks = state.check_pool_for_new_blocks(net_phs, odds, primary_coin)
            for block in pool_new_blocks:
                worker = block.get("worker") or block["miner"]
                block_hash_pool = block.get("hash", "")
                # Skip if already alerted via miner-reported detection — check both worker
                # name and block hash since CGMiner may report a different name than pool API
                if worker in alerted_workers_this_cycle or (block_hash_pool and block_hash_pool in alerted_hashes_this_cycle):
                    logger.debug(f"Pool block for {worker} already alerted via miner-reported detection")
                    continue

                state.pool_blocks_found += 1
                state.record_block_time()
                # Build miner details for the block embed
                # Case-insensitive comparison: pool API worker name may differ in case from miner name
                miner_details = {}
                for mname, hr in md.items():
                    miner_details[mname] = {
                        "hashrate": hr,
                        "online": miner_status.get(mname) != "offline",
                        "power": power.get(mname, 0),
                        "found_block": mname.lower() == worker.lower()
                    }
                send_alert("block_found", create_block_embed(
                    block["height"], prices, bri, worker,
                    miner_details=miner_details,
                    pool_block_num=state.pool_blocks_found,
                    coin_symbol=block.get("coin", primary_coin),
                    block_hash=block.get("hash"),
                    network_hashrate=net_phs,
                    difficulty=diff,
                    observed_hashrate_hs=fleet_ths * 1e12,
                ), state)
                trigger_block_celebration(miner_details)
                logger.info(f"POOL BLOCK #{state.pool_blocks_found} by {sanitize_log_input(worker)} (height {block['height']})!")

            # ═══════════════════════════════════════════════════════════════════════════════
            # P0 AUDIT FIX: ORPHAN DETECTION - Check for orphaned blocks every cycle
            # ═══════════════════════════════════════════════════════════════════════════════
            # This queries the pool API and compares block statuses to detect orphans.
            # Orphan alerts bypass quiet hours and batching (IMMEDIATE_ALERT_TYPES).
            orphans = state.check_for_orphans()
            for orphan in orphans:
                send_alert("block_orphaned", create_block_orphaned_embed(
                    block_height=orphan["height"],
                    pool_block_num=None,  # We don't track pool block number for orphans
                    coin_symbol=orphan.get("coin", primary_coin),
                    found_at=orphan.get("found_at"),
                    orphaned_at=datetime.fromtimestamp(orphan.get("orphaned_at", time.time()), tz=get_display_tz()),
                    block_reward=orphan.get("reward"),
                    prices=prices
                ), state)
                logger.warning(f"ORPHAN ALERT SENT: Block {orphan['height']} ({orphan['hash'][:16]}...)")

            # ═══════════════════════════════════════════════════════════════════════════════
            # MERGE-MINING AUX CHAIN BLOCK DETECTION
            # ═══════════════════════════════════════════════════════════════════════════════
            # When mining a parent chain (BTC/LTC), also check aux chain pools for blocks.
            # Aux chains only use pool-side detection (miner found_blocks is coin-blind).
            if is_parent_chain(primary_coin):
                aux_pools = discover_active_aux_pools(primary_coin)
                for aux in aux_pools:
                    aux_symbol = aux["symbol"]
                    aux_pool_id = aux["pool_id"]

                    aux_blocks = state.check_pool_for_new_blocks(
                        net_phs, odds,
                        coin_symbol=aux_symbol,
                        pool_id=aux_pool_id
                    )

                    for block in aux_blocks:
                        worker = block.get("worker") or block["miner"]
                        aux_coin = block.get("coin", aux_symbol)

                        state.pool_blocks_found += 1
                        state.record_block_time()

                        # Fetch aux-specific price and reward
                        aux_prices = fetch_coin_price(aux_coin)
                        aux_bri = fetch_block_reward_for_coin(aux_coin)

                        miner_details = {}
                        for mname, hr in md.items():
                            miner_details[mname] = {
                                "hashrate": hr,
                                "online": miner_status.get(mname) != "offline",
                                "power": power.get(mname, 0),
                                "found_block": mname == worker
                            }
                        # Fetch aux chain network stats for the embed
                        _aux_net = fetch_network_stats(aux_coin)
                        send_alert("block_found", create_block_embed(
                            block["height"], aux_prices, aux_bri, worker,
                            miner_details=miner_details,
                            pool_block_num=state.pool_blocks_found,
                            coin_symbol=aux_coin,
                            block_hash=block.get("hash"),
                            network_hashrate=_aux_net.get("network_phs") if _aux_net else None,
                            difficulty=_aux_net.get("difficulty") if _aux_net else None,
                        ), state)
                        trigger_block_celebration(miner_details)
                        logger.info(
                            f"AUX BLOCK #{state.pool_blocks_found} ({aux_coin}) "
                            f"by {sanitize_log_input(worker)} (height {block['height']})!"
                        )

                # Aux chain orphan detection
                for aux in aux_pools:
                    aux_orphans = state.check_for_orphans(pool_id=aux["pool_id"])
                    for orphan in aux_orphans:
                        orphan_coin = orphan.get("coin", aux["symbol"])
                        orphan_prices = fetch_coin_price(orphan_coin)
                        send_alert("block_orphaned", create_block_orphaned_embed(
                            block_height=orphan["height"],
                            coin_symbol=orphan_coin,
                            found_at=orphan.get("found_at"),
                            orphaned_at=datetime.fromtimestamp(orphan.get("orphaned_at", time.time()), tz=get_display_tz()),
                            block_reward=orphan.get("reward"),
                            prices=orphan_prices
                        ), state)
                        logger.warning(
                            f"AUX ORPHAN ALERT: {aux['symbol']} block {orphan['height']} "
                            f"({orphan['hash'][:16]}...)"
                        )

            # HIGH ODDS alert - check ALL enabled coins (primary + merge-minable aux chains)
            # One alert per "high odds episode": fires once when sustained, then stays silent
            # until odds drop below threshold and come back up. 6h report covers ongoing visibility.
            HIGH_ODDS_SUSTAIN = 3600    # Must be sustained for 1 hour (3600 seconds)
            if not in_startup_grace:
                # Build list: primary coin (data already fetched) + other enabled coins
                _ho_coins = [(primary_coin, net_phs, odds)]
                for _ho_cfg in get_enabled_coins():
                    _ho_sym = _ho_cfg.get("symbol", "").upper()
                    if _ho_sym and _ho_sym != primary_coin:
                        _ho_net = fetch_network_stats(_ho_sym)
                        if _ho_net and _ho_net.get("network_phs", 0) > 0:
                            _ho_odds = calc_odds(_ho_net["network_phs"], fleet_ths, _ho_sym)
                            _ho_coins.append((_ho_sym, _ho_net["network_phs"], _ho_odds))

                for _ho_sym, _ho_net_phs, _ho_odds in _ho_coins:
                    _ho_pct = _ho_odds.get("daily_odds_pct", 0)
                    if _ho_pct >= ODDS_TH:
                        odds_now = time.time()

                        # Track when high odds condition was first detected for this coin
                        if state.high_odds_first_detected.get(_ho_sym) is None:
                            state.high_odds_first_detected[_ho_sym] = odds_now

                        # One alert per episode: skip if already alerted during this high-odds session
                        already_alerted = state.high_odds_session_alerted.get(_ho_sym, False)

                        # Hard 6-hour cooldown — prevents re-firing when odds briefly dip and recover
                        HIGH_ODDS_COOLDOWN = 6 * 3600
                        last_alert_time = state.high_odds_last_alert.get(_ho_sym, 0)
                        on_cooldown = (odds_now - last_alert_time) < HIGH_ODDS_COOLDOWN

                        # Only send if: sustained for 1 hour AND not already alerted AND not on cooldown
                        sustained_duration = odds_now - state.high_odds_first_detected[_ho_sym]
                        if sustained_duration >= HIGH_ODDS_SUSTAIN and not already_alerted and not on_cooldown:
                            if send_alert("high_odds", create_high_odds_embed(_ho_pct, _ho_net_phs, _ho_odds.get("days_per_block", float('inf')), _ho_sym), state):
                                state.high_odds_last_alert[_ho_sym] = odds_now
                                state.high_odds_session_alerted[_ho_sym] = True
                    else:
                        # Odds dropped below threshold — reset for next episode
                        state.high_odds_first_detected[_ho_sym] = None
                        state.high_odds_session_alerted[_ho_sym] = False

            # === NETWORK HASHRATE CRASH DETECTION ===
            # Alert if: 25%+ drop sustained for 30 minutes
            if not in_startup_grace:
                # Check for crash conditions first, then update baseline
                # (baseline must NOT adapt during a crash or it self-resolves)
                if state.network_baseline_phs is None:
                    state.network_baseline_phs = net_phs

                if state.network_baseline_phs > 0:
                    drop_pct = ((state.network_baseline_phs - net_phs) / state.network_baseline_phs) * 100

                    # Crash detected if hashrate drops 25%+ from baseline
                    is_crash = drop_pct >= NET_CRASH_PCT

                    if is_crash:
                        if state.network_crash_first_detected is None:
                            state.network_crash_first_detected = time.time()
                        elif not state.network_crash_alert_sent and (time.time() - state.network_crash_first_detected) >= NET_CRASH_SUSTAIN:
                            # Sustained crash - send alert
                            send_alert("hashrate_crash", create_hashrate_crash_embed(net_phs, state.network_baseline_phs, drop_pct), state)
                            state.network_crash_alert_sent = True
                            logger.error(f"HASHRATE CRASH: {net_phs:.2f} PH/s (was {state.network_baseline_phs:.2f}, -{drop_pct:.1f}%)")
                    else:
                        # Reset crash detection if conditions no longer met
                        state.network_crash_first_detected = None
                        # Reset alert flag when hashrate recovers to within 10% of baseline
                        if drop_pct < (NET_CRASH_PCT * 0.4):  # Recovery threshold: less than 10% drop
                            state.network_crash_alert_sent = False
                        # Only update baseline when NOT in crash territory
                        # This prevents the baseline from drifting down during sustained crashes
                        state.network_baseline_phs = state.network_baseline_phs * 0.9 + net_phs * 0.1

            # === POOL HASHRATE DROP DETECTION ===
            # Alert if: 50%+ drop from expected AND sustained for 15 minutes
            # Skip if user disabled this feature (didn't set expected hashrate)
            if not in_startup_grace and state.expected_fleet_ths > 0 and not state.expected_fleet_ths_disabled:
                drop_pct = ((state.expected_fleet_ths - fleet_ths) / state.expected_fleet_ths) * 100
                is_pool_drop = drop_pct >= POOL_DROP_PCT

                if is_pool_drop:
                    if state.pool_drop_first_detected is None:
                        state.pool_drop_first_detected = time.time()
                    elif not state.pool_drop_alert_sent and (time.time() - state.pool_drop_first_detected) >= POOL_DROP_SUSTAIN:
                        # Sustained drop - send alert with miner details
                        miner_details = {}
                        for mname, hr in md.items():
                            miner_details[mname] = {
                                "hashrate": hr,
                                "online": miner_status.get(mname) != "offline",
                                "power": power.get(mname, 0)
                            }
                        send_alert("pool_hashrate_drop", create_pool_hashrate_drop_embed(
                            fleet_ths, state.expected_fleet_ths, drop_pct, miner_details
                        ), state)
                        state.pool_drop_alert_sent = True
                        logger.warning(f"POOL DROP: {fleet_ths:.2f} TH/s (expected {state.expected_fleet_ths:.2f}, -{drop_pct:.1f}%)")
                else:
                    # Reset pool drop detection if fleet recovers
                    state.pool_drop_first_detected = None
                    if fleet_ths >= state.expected_fleet_ths * 0.6:  # 60% of expected = recovered
                        state.pool_drop_alert_sent = False

            # Miner offline/online & auto-restart
            # M-4: Uses hysteresis to prevent flapping alerts
            for name, st in miner_status.items():
                if st == "offline":
                    # Miner is offline - reset stable online timer
                    state.miner_stable_online_since.pop(name, None)

                    if name not in state.miner_offline_since: state.miner_offline_since[name] = time.time()
                    mins = int((time.time() - state.miner_offline_since[name]) / 60)
                    if mins >= MINER_OFFLINE_TH and name not in state.miner_offline_alert_sent:
                        send_alert("miner_offline", create_miner_offline_embed(name, mins, miner_ip=_miner_ip_lookup.get(name)), state, miner_name=name)
                        state.track_chronic_issue(name, "miner_offline")  # Track for chronic detection
                        state.miner_offline_alert_sent[name] = True
                        state.weekly_stats["offline_events"] += 1
                    # Auto-restart (only if health monitoring enabled)
                    if HEALTH_MONITORING_ENABLED and AUTO_RESTART and mins >= AUTO_RESTART_MIN and (time.time() - state.get_last_restart_time(name)) > AUTO_RESTART_COOL and not in_startup_grace:
                        logger.warning(f"OFFLINE RESTART: {sanitize_log_input(name)}")
                        ok = False
                        # Try all miner types with universal restart function
                        ALL_MINER_TYPES = ["nerdqaxe", "nmaxe", "axeos", "avalon", "antminer", "antminer_scrypt", "whatsminer", "innosilicon", "futurebit", "hammer", "goldshell", "luckyminer", "jingleminer", "zyber", "esp32miner"]
                        for miner_type in ALL_MINER_TYPES:
                            for m in MINERS.get(miner_type, []):
                                if m["name"] == name:
                                    ok = restart_miner(miner_type, m["ip"], m.get("port", 4028))
                                    break
                            if ok:
                                break
                        if ok:
                            send_alert("auto_restart", create_restart_embed(name, mins, ok), state, miner_name=name)
                            state.record_miner_restart(name)  # Record restart time to enforce cooldown
                        else:
                            logger.warning(f"Auto-restart failed for {sanitize_log_input(name)} - miner may not support remote restart")
                else:
                    # Miner is online - apply hysteresis before clearing offline state
                    if name in state.miner_offline_since:
                        # Start or check stable online timer (M-4 hysteresis)
                        if name not in state.miner_stable_online_since:
                            state.miner_stable_online_since[name] = time.time()

                        stable_secs = time.time() - state.miner_stable_online_since[name]
                        if stable_secs >= state.hysteresis_threshold_sec:
                            # Miner has been stable online long enough - clear offline state
                            mins = int((time.time() - state.miner_offline_since[name]) / 60)
                            if mins >= MINER_OFFLINE_TH:
                                send_alert("miner_online", create_miner_online_embed(
                                    name, mins,
                                    miner_ip=_miner_ip_lookup.get(name),
                                    hashrate_ghs=md.get(name, 0),
                                    temp_c=temps.get(name, {}).get("chip", temps.get(name, {}).get("board", 0))
                                ), state, miner_name=name)
                            del state.miner_offline_since[name]
                            state.miner_offline_alert_sent.pop(name, None)
                            state.miner_stable_online_since.pop(name, None)
                            # Only record restart if uptime confirms actual restart (not just connectivity blip)
                            miner_uptime = uptimes.get(name, 0)
                            if miner_uptime < 600:  # Uptime < 10 min = actual restart occurred
                                state.record_miner_restart(name)
                        # else: miner came online but not stable yet - keep tracking
                    else:
                        # Miner was never offline - clear any stale hysteresis timer
                        state.miner_stable_online_since.pop(name, None)

            # Temp alerts (skip Avalon devices - they are personal heaters, high temps expected)
            # Enhanced with thermal protection: emergency_stop_axeos at TEMP_EMERGENCY or sustained TEMP_CRIT
            for name, td in temps.items():
                if is_avalon_miner(name):
                    continue
                ct = td.get("chip", td.get("board", 0))
                miner_ip = _miner_ip_lookup.get(name)
                miner_type = _miner_type_lookup.get(name, "unknown")
                is_axeos = miner_type in ["axeos", "bitaxe", "nmaxe", "nerdaxe", "nerdqaxe", "nerdoctaxe", "qaxe", "qaxeplus", "hammer", "esp32miner", "luckyminer", "jingleminer", "zyber"]

                # ── THERMAL SHUTDOWN LOGIC ──
                if THERMAL_SHUTDOWN_ENABLED and name not in state.thermal_shutdown_sent:
                    # Tier 1: Immediate stop at TEMP_EMERGENCY (95°C default)
                    if ct >= TEMP_EMERGENCY:
                        stopped = False
                        if is_axeos and miner_ip:
                            stopped = emergency_stop_axeos(miner_ip)
                        send_alert("thermal_shutdown", create_thermal_shutdown_embed(name, ct, stopped, miner_type), state, miner_name=name)
                        state.thermal_shutdown_sent[name] = True
                        state.thermal_critical_since.pop(name, None)  # Clear sustained tracking
                        logger.warning(f"THERMAL SHUTDOWN: {sanitize_log_input(name)} at {ct}°C (immediate, stopped={stopped})")

                    # Tier 2: Sustained TEMP_CRIT for THERMAL_SHUTDOWN_SUSTAINED_SEC (90s default)
                    elif ct >= TEMP_CRIT:
                        if name not in state.thermal_critical_since:
                            state.thermal_critical_since[name] = time.time()
                        elif (time.time() - state.thermal_critical_since[name]) >= THERMAL_SHUTDOWN_SUSTAINED_SEC:
                            stopped = False
                            if is_axeos and miner_ip:
                                stopped = emergency_stop_axeos(miner_ip)
                            send_alert("thermal_shutdown", create_thermal_shutdown_embed(name, ct, stopped, miner_type), state, miner_name=name)
                            state.thermal_shutdown_sent[name] = True
                            state.thermal_critical_since.pop(name, None)
                            logger.warning(f"THERMAL SHUTDOWN: {sanitize_log_input(name)} at {ct}°C (sustained {THERMAL_SHUTDOWN_SUSTAINED_SEC}s, stopped={stopped})")

                # ── EXISTING TEMP ALERTS (notification-only, separate from shutdown) ──
                if ct >= TEMP_CRIT and name not in state.temp_alert_sent:
                    send_alert("temp_critical", create_temp_embed(name, ct, "CRITICAL", miner_ip=miner_ip), state, miner_name=name)
                    state.track_chronic_issue(name, "temp_critical")
                    state.temp_alert_sent[name] = True
                elif ct >= TEMP_WARN and name not in state.temp_alert_sent:
                    send_alert("temp_warning", create_temp_embed(name, ct, "WARNING", miner_ip=miner_ip), state, miner_name=name)
                    state.track_chronic_issue(name, "temp_warning")
                    state.temp_alert_sent[name] = True
                elif ct < TEMP_WARN - 5:
                    state.temp_alert_sent.pop(name, None)
                    # Clear thermal tracking when temp drops well below warning
                    state.thermal_critical_since.pop(name, None)
                    state.thermal_shutdown_sent.pop(name, None)

            # ═══════════════════════════════════════════════════════════════════════════════
            # B1: FAN FAILURE DETECTION - Alert when any fan RPM = 0 while miner is running
            # ═══════════════════════════════════════════════════════════════════════════════
            if HEALTH_MONITORING_ENABLED and not in_startup_grace:
                for name, fan_speeds in fans.items():
                    if is_avalon_miner(name):
                        continue  # Skip Avalons - may not report fans reliably
                    if not fan_speeds or miner_status.get(name) != "online":
                        continue
                    chip_temp = temps.get(name, {}).get("chip", temps.get(name, {}).get("board", 0))
                    # Only alert if any fan is at 0 RPM and chip temp > 40°C (miner is actually running)
                    has_dead_fan = any(rpm == 0 for rpm in fan_speeds)
                    if has_dead_fan and chip_temp > 40 and name not in state.fan_alert_sent:
                        alert_key = f"fan_failure:{name}"
                        last_alert = state.last_alerts.get(alert_key, 0)
                        cooldown = ALERT_COOLDOWNS.get("fan_failure", 1800)
                        if (current_time - last_alert) >= cooldown:
                            send_alert("fan_failure", create_fan_failure_embed(name, fan_speeds, chip_temp), state, miner_name=name)
                            state.last_alerts[alert_key] = current_time
                            state.fan_alert_sent[name] = True
                            logger.warning(f"FAN FAILURE: {sanitize_log_input(name)} fans={fan_speeds} chip={chip_temp}°C")
                    elif not has_dead_fan:
                        state.fan_alert_sent.pop(name, None)

            # ═══════════════════════════════════════════════════════════════════════════════
            # STRATUM URL MISMATCH - Security alert for firmware hijack detection
            # ═══════════════════════════════════════════════════════════════════════════════
            if not in_startup_grace:
                # Build expected pool URLs from config
                expected_pool_urls = set()
                pool_url = CONFIG.get("pool_url", "")
                if pool_url:
                    expected_pool_urls.add(pool_url.lower().rstrip("/"))
                # Also accept any configured fallback/backup pool URLs
                for fb_url in CONFIG.get("fallback_pool_urls", []):
                    if fb_url:
                        expected_pool_urls.add(fb_url.lower().rstrip("/"))

                if expected_pool_urls:
                    for name, pool in mpools.items():
                        if not pool:
                            continue
                        pool_normalized = pool.lower().rstrip("/")
                        # Check if any expected URL is a substring of the actual URL (handles port differences)
                        is_expected = any(exp in pool_normalized or pool_normalized in exp for exp in expected_pool_urls)
                        if not is_expected:
                            # First time seeing this miner? Record baseline, don't alert
                            if name not in state.known_miner_pool_urls:
                                state.known_miner_pool_urls[name] = pool_normalized
                            elif name not in state.url_mismatch_alerted or state.url_mismatch_alerted[name] != pool_normalized:
                                # Re-alert if URL changed to a DIFFERENT bad URL (secondary hijack detection)
                                alert_key = f"stratum_url_mismatch:{name}"
                                last_alert = state.last_alerts.get(alert_key, 0)
                                if (current_time - last_alert) >= ALERT_COOLDOWNS.get("stratum_url_mismatch", 0):
                                    send_alert("stratum_url_mismatch", create_stratum_url_mismatch_embed(
                                        name, list(expected_pool_urls)[0] if expected_pool_urls else "unknown", pool
                                    ), state, miner_name=name)
                                    state.last_alerts[alert_key] = current_time
                                    state.url_mismatch_alerted[name] = pool_normalized  # Store URL, not True
                                    logger.warning(f"STRATUM URL MISMATCH: {sanitize_log_input(name)} pointing to {sanitize_log_input(pool)} instead of expected pool")
                        else:
                            state.url_mismatch_alerted.pop(name, None)
                            state.known_miner_pool_urls[name] = pool_normalized

            # ═══════════════════════════════════════════════════════════════════════════════
            # HASHBOARD DEATH DETECTION - Alert when per-chain hashrate drops to 0
            # ═══════════════════════════════════════════════════════════════════════════════
            if HEALTH_MONITORING_ENABLED and not in_startup_grace:
                for name, chains in chain_data.items():
                    if miner_status.get(name) != "online":
                        continue
                    # Find dead chains (0 hashrate) while at least one chain is hashing
                    active_chains = [i for i, hr in enumerate(chains) if hr > 0]
                    dead_chains = [i for i, hr in enumerate(chains) if hr == 0]
                    if dead_chains and active_chains and name not in state.hashboard_alert_sent:
                        alert_key = f"hashboard_dead:{name}"
                        last_alert = state.last_alerts.get(alert_key, 0)
                        cooldown = ALERT_COOLDOWNS.get("hashboard_dead", 3600)
                        if (current_time - last_alert) >= cooldown:
                            send_alert("hashboard_dead", create_hashboard_dead_embed(name, chains, dead_chains), state, miner_name=name)
                            state.last_alerts[alert_key] = current_time
                            state.hashboard_alert_sent[name] = True
                            logger.warning(f"HASHBOARD DEAD: {sanitize_log_input(name)} chains={chains} dead={dead_chains}")
                    elif not dead_chains:
                        state.hashboard_alert_sent.pop(name, None)

            # ═══════════════════════════════════════════════════════════════════════════════
            # HARDWARE ERROR RATE - Predictive ASIC chip failure detection
            # ═══════════════════════════════════════════════════════════════════════════════
            if HEALTH_MONITORING_ENABLED and not in_startup_grace:
                for name, total_hw_errors in hw_errors_data.items():
                    if miner_status.get(name) != "online":
                        continue
                    # Track HW error history (like stale tracking)
                    if name not in state.miner_hw_errors:
                        state.miner_hw_errors[name] = []
                    hw_history = state.miner_hw_errors[name]
                    # Detect counter reset
                    if hw_history and total_hw_errors < hw_history[-1].get("total", 0):
                        state.miner_hw_errors[name] = []
                        hw_history = state.miner_hw_errors[name]
                    hw_history.append({"t": time.time(), "total": total_hw_errors})
                    state.miner_hw_errors[name] = hw_history[-20:]  # Keep last 20 samples

                    # Calculate errors per hour from wider window (avoids single-sample false positives)
                    if len(hw_history) >= 5:
                        newest = hw_history[-1]
                        oldest = hw_history[max(0, len(hw_history) - 10)]  # Up to 10 samples back (~20 min)
                        dt_hours = (newest["t"] - oldest["t"]) / 3600
                        if dt_hours > 0:
                            delta_errors = newest["total"] - oldest["total"]
                            errors_per_hour = delta_errors / dt_hours
                            # Alert if > 25 HW errors per hour (configurable)
                            hw_threshold = CONFIG.get("hw_error_rate_threshold", 25)
                            if errors_per_hour > hw_threshold and name not in state.hw_error_alert_sent:
                                alert_key = f"hw_error_rate:{name}"
                                last_alert = state.last_alerts.get(alert_key, 0)
                                cooldown = ALERT_COOLDOWNS.get("hw_error_rate", 3600)
                                if (current_time - last_alert) >= cooldown:
                                    send_alert("hw_error_rate", create_hw_error_rate_embed(name, errors_per_hour, total_hw_errors), state, miner_name=name)
                                    state.last_alerts[alert_key] = current_time
                                    state.hw_error_alert_sent[name] = True
                                    logger.warning(f"HW ERROR RATE: {sanitize_log_input(name)} {errors_per_hour:.0f}/hr (total: {total_hw_errors})")
                            elif errors_per_hour <= hw_threshold / 2:
                                state.hw_error_alert_sent.pop(name, None)

            # Blip / Miner Reboot detection
            if BLIP_ENABLED:
                blips_this_cycle = []
                blip_details = {}
                for name, uptime in uptimes.items():
                    # Skip invalid uptime values (0, negative, or unreasonably high)
                    if not isinstance(uptime, (int, float)) or uptime <= 0 or uptime > 31536000:  # Max 1 year
                        logger.debug(f"Skipping invalid uptime for {sanitize_log_input(name)}: {uptime}")
                        continue
                    last = state.miner_last_uptime.get(name, uptime)
                    if name not in state.miner_last_uptime and uptime < 300:
                        state.record_miner_restart(name)  # Use new list-based tracking
                    if uptime < last and last > 300:
                        state.record_blip(name)
                        blips_this_cycle.append(name)
                        blip_details[name] = {"old": last, "new": uptime}
                        state.record_miner_restart(name)  # Use new list-based tracking
                    state.miner_last_uptime[name] = uptime
                power_event = state.check_power_event()
                if power_event and power_event["count"] >= 2:
                    # Multiple miners rebooted - likely power event
                    send_alert("power_event", create_power_event_embed(power_event), state)
                elif blips_this_cycle:
                    # Individual miner reboots - use per-miner cooldown to avoid global key blocking
                    for name in blips_this_cycle:
                        alert_key = f"miner_reboot:{name}"
                        last_alert = state.last_alerts.get(alert_key, 0)
                        cooldown = ALERT_COOLDOWNS.get("miner_reboot", 600)
                        if (current_time - last_alert) >= cooldown:
                            details = blip_details.get(name, {})
                            old_uptime = details.get("old", 0)
                            new_uptime = details.get("new", 0)
                            send_alert("miner_reboot", create_miner_reboot_embed(name, old_uptime, new_uptime), state, miner_name=name)
                            state.last_alerts[alert_key] = current_time
                        state.track_chronic_issue(name, "miner_reboot")  # Track for chronic detection

            # Hashrate degradation (skip ESP32 — pool-reported hashrate fluctuates wildly at kH/s scale)
            for name, hr_ghs in miner_hashrates.items():
                if _miner_type_lookup.get(name) == "esp32miner":
                    continue
                if hr_ghs > 0 and (time.time() - state.get_last_restart_time(name)) > 900:  # 15 min cooldown
                    deg = state.update_hashrate_baseline(name, hr_ghs)
                    if deg:
                        _deg_temp = temps.get(name, {}).get("chip", temps.get(name, {}).get("board", 0))
                        send_alert("degradation", create_degradation_embed(deg, temp_c=_deg_temp or None), state, miner_name=name)
                        state.track_chronic_issue(name, "degradation")  # Track for chronic detection

            # === EXCESSIVE RESTART DETECTION ===
            # Alert if miner reboots too frequently (indicates hardware/power issues)
            if HEALTH_MONITORING_ENABLED and not in_startup_grace:
                for name in miner_status.keys():
                    is_excessive, count, window = state.check_excessive_restarts(name, threshold=3, window_hours=1)
                    if is_excessive:
                        # Use miner_reboot key for cooldown but track separately
                        restart_alert_key = f"excessive_restart:{name}"
                        last_alert = state.last_alerts.get(restart_alert_key, 0)
                        if (time.time() - last_alert) > 3600:  # 1 hour cooldown between excessive restart alerts
                            send_alert("excessive_restarts", create_excessive_restarts_embed(name, count, window), state, miner_name=name)
                            state.last_alerts[restart_alert_key] = time.time()
                            logger.warning(f"EXCESSIVE RESTARTS: {sanitize_log_input(name)} restarted {count}x in {window}h")

            # === CHRONIC ISSUE DETECTION ===
            # Flag miners with repeatedly recurring issues
            if HEALTH_MONITORING_ENABLED and not in_startup_grace:
                for name in miner_status.keys():
                    is_chronic, issue_info = state.check_chronic_issues(name)
                    if is_chronic:
                        send_alert("chronic_issue", create_chronic_issue_embed(name, issue_info), state)
                        logger.warning(f"CHRONIC ISSUE: {sanitize_log_input(name)} - {sanitize_log_input(issue_info['type'])} occurred {issue_info['count']}x")

            # === POOL VS MINER HASHRATE DIVERGENCE ===
            # Alert if pool sees significantly less hashrate than miner claims
            if HEALTH_MONITORING_ENABLED and CONFIG.get("pool_share_validation", True) and not in_startup_grace:
                pool_stats, has_per_worker = get_pool_share_stats()

                if has_per_worker:
                    # Per-worker mode: check each miner individually against pool
                    for name, hr_ghs in miner_hashrates.items():
                        # Use multi-identifier matching (same as pool share tracking)
                        miner_ids = worker_names.get(name, {})
                        pool_worker = None
                        for candidate in [miner_ids.get("worker"), miner_ids.get("hostname"), miner_ids.get("ip"), name]:
                            if candidate and pool_stats and candidate in pool_stats:
                                pool_worker = candidate
                                break

                        if pool_worker:
                            pool_hr = pool_stats[pool_worker].get("hashrate", 0)
                        else:
                            # Can't match miner to pool - skip divergence tracking to avoid false alerts
                            continue
                        # Track variance (pool hashrate in GH/s)
                        state.track_hashrate_variance(name, hr_ghs, pool_hr)
                        # Check for persistent divergence (6+ consecutive checks with <50% received)
                        is_divergent, miner_hr, pool_hr_val, diverge_count = state.check_hashrate_divergence(name, threshold_count=6)
                        if is_divergent:
                            diverge_alert_key = f"hashrate_divergence:{name}"
                            last_alert = state.last_alerts.get(diverge_alert_key, 0)
                            if (time.time() - last_alert) > 1800:  # 30 min cooldown
                                send_alert("hashrate_divergence", create_hashrate_divergence_embed(name, miner_hr, pool_hr_val, diverge_count), state)
                                state.last_alerts[diverge_alert_key] = time.time()
                                logger.warning(f"HASHRATE DIVERGENCE: {sanitize_log_input(name)} claims {miner_hr:.0f} GH/s but pool sees {pool_hr_val:.0f} GH/s")
                else:
                    # Wallet-level aggregate mode (Spiral Pool): compare total fleet to pool aggregate
                    # This detects if miners aren't submitting shares even without per-worker breakdown
                    wallet = CONFIG.get("wallet_address", "")
                    pool_aggregate_hr = 0
                    for worker_id, stats in pool_stats.items():
                        if worker_id == wallet or worker_id.startswith(wallet):
                            pool_aggregate_hr = stats.get("hashrate", 0)
                            break

                    # Convert pool hashrate from H/s to GH/s for comparison
                    pool_aggregate_ghs = pool_aggregate_hr / 1e9 if pool_aggregate_hr > 1e6 else pool_aggregate_hr

                    # Track aggregate variance (fleet vs pool)
                    state.track_hashrate_variance("_fleet_aggregate", fleet_ths * 1000, pool_aggregate_ghs)  # fleet_ths to GH/s

                    # Check for persistent divergence at fleet level
                    is_divergent, fleet_hr, pool_hr_val, diverge_count = state.check_hashrate_divergence("_fleet_aggregate", threshold_count=6)
                    if is_divergent and fleet_hr > 0:
                        diverge_alert_key = "hashrate_divergence:_fleet_aggregate"
                        last_alert = state.last_alerts.get(diverge_alert_key, 0)
                        if (time.time() - last_alert) > 1800:  # 30 min cooldown
                            # Create fleet-level divergence alert
                            send_alert("hashrate_divergence", create_hashrate_divergence_embed(
                                "Fleet Total", fleet_hr, pool_hr_val, diverge_count
                            ), state)
                            state.last_alerts[diverge_alert_key] = time.time()
                            logger.warning(f"FLEET HASHRATE DIVERGENCE: Fleet claims {fleet_hr:.0f} GH/s but pool sees {pool_hr_val:.0f} GH/s")

            # === CONSOLIDATED REPORT SYSTEM ===
            # Detects which reports are due and sends a single consolidated message
            # instead of multiple separate Discord notifications
            report_types = []
            wn = now.isocalendar()[1]
            mn = now.month
            qn = get_quarter_name()
            special_info = get_special_date_info()
            date_key = f"{now.month}-{now.day}"

            # Check which reports are due
            # Support 6-hour (4x daily), daily (1x daily), or off report modes
            # Each report type can also be individually disabled
            #
            # CRITICAL FIX: Use date+hour key to prevent cross-day deduplication issues
            # Example: If Sentinel ran yesterday at 18:00 and restarts today at 18:05,
            # we still want to send the 18:00 report for TODAY.
            report_hour_key = now.strftime("%Y-%m-%d-") + str(now.hour)  # e.g., "2026-01-09-6"

            if REPORT_FREQUENCY == "off" or not ENABLE_6H_REPORTS:
                is_6h_due = False
            elif REPORT_FREQUENCY == "daily":
                today_key = now.strftime("%Y-%m-%d")
                in_window = (now.hour == MAJOR_REPORT_HOUR and now.minute < REPORT_WINDOW)
                # Catch-up: if we missed the report window (e.g., Sentinel was down), send within 1 hour
                missed_catchup = (not first_report_cycle and state.last_daily_report is not None
                                  and state.last_daily_report != today_key
                                  and now.hour > MAJOR_REPORT_HOUR
                                  and (now.hour - MAJOR_REPORT_HOUR) <= 1)
                is_6h_due = (state.last_daily_report != today_key and (in_window or missed_catchup))
            else:
                # Wall-clock based: fire at hours in REPORT_HOURS (6, 12, 18) plus FINAL_REPORT_TIME (21:45)
                # Primary window: first REPORT_WINDOW minutes of that hour
                # Final report window: FINAL_REPORT_WINDOW minutes starting at the exact time
                # Catch-up: if we missed a report hour entirely, send it on the next check
                in_report_hour = now.hour in REPORT_HOURS
                not_yet_sent = state.last_report_hour != report_hour_key
                in_primary_window = now.minute < REPORT_WINDOW

                # Check for final report time (e.g., 21:45 before quiet hours)
                is_final_report_time = False
                final_report_key = None
                if FINAL_REPORT_TIME:
                    final_hour, final_minute = FINAL_REPORT_TIME
                    final_report_key = now.strftime("%Y-%m-%d-") + f"final-{final_hour}:{final_minute:02d}"
                    # Check if we're in the final report window
                    if now.hour == final_hour and final_minute <= now.minute < final_minute + FINAL_REPORT_WINDOW:
                        is_final_report_time = True
                        not_yet_sent = state.last_report_hour != final_report_key
                        if not_yet_sent:
                            report_hour_key = final_report_key  # Use final report key for deduplication
                            logger.info(f"Final report time reached: {final_hour}:{final_minute:02d}")

                # Check if we missed a report - find the most recent report hour we should have hit
                # SKIP catch-up on first cycle to avoid sending "missed" report on fresh startup
                # ALSO skip catch-up if last_report_hour is None (fresh install, no prior reports)
                # LIMIT catch-up to within 1 hour of missed report time (no stale catch-ups)
                missed_report_key = None
                # Only attempt catch-up if we have a valid last_report_hour (not a fresh start)
                has_prior_reports = state.last_report_hour is not None
                if not in_report_hour and not is_final_report_time and not_yet_sent and not first_report_cycle and has_prior_reports:
                    # Find the last report hour that should have fired
                    for rh in sorted(REPORT_HOURS, reverse=True):
                        if rh < now.hour:
                            # Only catch up if within 1 hour of the missed report time
                            hours_since_missed = now.hour - rh
                            if hours_since_missed <= 1:
                                missed_report_key = now.strftime("%Y-%m-%d-") + str(rh)
                            break
                    # NOTE: Yesterday's catch-up removed - always > 1 hour stale (min 6h from 18:00 to 00:00)

                    # Catch up missed final report (e.g., Sentinel restarted during/after the 21:45 window)
                    # The regular REPORT_HOURS loop above won't find it — check explicitly.
                    if not missed_report_key and FINAL_REPORT_TIME:
                        fh, fm = FINAL_REPORT_TIME
                        f_key = now.strftime("%Y-%m-%d-") + f"final-{fh}:{fm:02d}"
                        if state.last_report_hour != f_key:
                            window_end_mins = fh * 60 + fm + FINAL_REPORT_WINDOW
                            now_mins = now.hour * 60 + now.minute
                            if window_end_mins <= now_mins <= window_end_mins + 60:
                                missed_report_key = f_key

                    # Only catch up if we actually missed it (wasn't sent)
                    if missed_report_key and state.last_report_hour != missed_report_key:
                        logger.info(f"Catching up missed report from {missed_report_key}")
                        report_hour_key = missed_report_key  # Use the missed hour's key
                        is_6h_due = True
                    else:
                        is_6h_due = False
                elif first_report_cycle and not in_report_hour and not is_final_report_time:
                    # First cycle - skip catch-up, just mark as not due
                    is_6h_due = False
                else:
                    # Due if in hourly window OR in final report window
                    is_6h_due = (in_report_hour and not_yet_sent and in_primary_window) or (is_final_report_time and not_yet_sent)
                    # Debug logging for report triggering
                    if in_report_hour or is_final_report_time:
                        logger.debug(f"Report check: hour={now.hour}:{now.minute:02d}, in_report_hour={in_report_hour}, "
                                    f"is_final_report_time={is_final_report_time}, not_yet_sent={not_yet_sent}, "
                                    f"last={state.last_report_hour}, key={report_hour_key}, is_6h_due={is_6h_due}")

            is_weekly_due = (ENABLE_WEEKLY_REPORTS and now.weekday() == WEEKLY_REPORT_DAY and now.hour == MAJOR_REPORT_HOUR and state.last_weekly_report != wn)
            is_monthly_due = (ENABLE_MONTHLY_REPORTS and now.day == MONTHLY_REPORT_DAY and now.hour == MAJOR_REPORT_HOUR and state.last_monthly_report != mn)
            is_quarterly_due = (ENABLE_QUARTERLY_REPORTS and is_quarter_end() and now.hour == MAJOR_REPORT_HOUR and state.last_quarterly_report != qn)
            is_special_due = (special_info and now.hour == MAJOR_REPORT_HOUR and state.last_special_date != date_key)

            # Monthly maintenance reminder - sent on 1st of each month at 8am
            # This is separate from other reports to ensure it's always visible
            maintenance_reminder_key = now.strftime("%Y-%m")  # YYYY-MM format
            is_maintenance_reminder_due = (now.day == 1 and now.hour == 8 and now.minute < REPORT_WINDOW and
                                           state.last_maintenance_reminder != maintenance_reminder_key)
            if is_maintenance_reminder_due:
                logger.info("Monthly maintenance reminder due - sending reminder")
                reminder_sent = send_alert("maintenance_reminder", create_maintenance_reminder_embed(), state)
                if reminder_sent:
                    state.last_maintenance_reminder = maintenance_reminder_key
                    logger.info(f"Monthly maintenance reminder sent for {maintenance_reminder_key}")

            if is_6h_due: report_types.append("6h")
            if is_weekly_due: report_types.append("weekly")
            if is_monthly_due: report_types.append("monthly")
            if is_quarterly_due: report_types.append("quarterly")
            if is_special_due: report_types.append("special")

            # Debug: Log report status at each report hour
            if now.hour in REPORT_HOURS and now.minute < 5:
                logger.info(f"Report status at {now.strftime('%H:%M')}: is_6h_due={is_6h_due}, "
                           f"REPORT_FREQUENCY={REPORT_FREQUENCY}, ENABLE_6H_REPORTS={ENABLE_6H_REPORTS}, "
                           f"last_report_hour={state.last_report_hour}, report_types={report_types}")

            # Send reports (consolidated if multiple, separate if single)
            if report_types:
                # Use coin-specific wallet balance
                coin_config = get_primary_coin_config()
                wallet_addr = coin_config.get("wallet_address", CONFIG.get("wallet_address", ""))
                wb = fetch_wallet_balance_for_coin(wallet_addr, primary_coin)
                trends = {
                    "network": net_trends.get("6h", {}).get("pct_change", 0) if net_trends.get("6h") else 0,
                    "fleet": fleet_trends.get("6h", {}).get("pct_change", 0) if fleet_trends.get("6h") else 0
                }

                if len(report_types) > 1:
                    # Multiple reports due - send consolidated
                    # Get infrastructure health for report (if metrics enabled)
                    infra_health_consolidated = get_infrastructure_health() if CONFIG.get("metrics_enabled", True) else None
                    data = {
                        "net_phs": net_phs,
                        "fleet_ths": fleet_ths,
                        "odds": odds,
                        "diff": diff,
                        "extremes": extremes,
                        "trends": trends,
                        "diff_trends": diff_trends,  # Difficulty trend data for multi-period analysis
                        "md": md,
                        "temps": temps,
                        "miner_status": miner_status,
                        "power": power,
                        "health_scores": health_scores,
                        "bri": bri,
                        "prices": prices,
                        "wallet_balance": wb,
                        "blocks_found": state.earnings.get("lifetime_blocks", 0),
                        "earnings": state.get_monthly_earnings(prices),
                        "weekly_stats": state.get_weekly_summary(),
                        "uptime": {n: state.get_uptime(n) for n in miner_status},
                        "quarterly_stats": state.get_quarterly_summary() if is_quarterly_due else {},
                        "quarter_name": qn,
                        "special_info": special_info if is_special_due else None,
                        "coin_symbol": primary_coin,  # Pass active coin to reports
                        "infra_health": infra_health_consolidated,  # Infrastructure health metrics
                        "aux_diff_trends": aux_diff_trends,  # Aux chain difficulty trends (merge mining)
                    }
                    embed = create_consolidated_report_embed(report_types, data)
                    # Use the highest priority alert type for cooldown tracking
                    alert_type = "quarterly_report" if is_quarterly_due else "weekly_report" if is_weekly_due else "monthly_earnings" if is_monthly_due else "6h_report"
                    report_sent = send_alert(alert_type, embed, state)
                    if report_sent:
                        logger.info(f"Consolidated report sent: {' + '.join(report_types)}")
                        # Update state for successfully sent consolidated reports
                        if is_6h_due:
                            state.last_report_hour = report_hour_key
                            if REPORT_FREQUENCY == "daily":
                                state.last_daily_report = now.strftime("%Y-%m-%d")
                        if is_weekly_due:
                            state.reset_weekly()
                            state.last_weekly_report = wn
                        if is_monthly_due:
                            state.reset_monthly()
                            state.last_monthly_report = mn
                        if is_quarterly_due:
                            state.reset_quarterly()
                            state.last_quarterly_report = qn
                        if is_special_due:
                            state.last_special_date = date_key
                    else:
                        logger.warning(f"Consolidated report send failed, will retry: {report_types}")
                else:
                    # Single report - use original embed functions for best formatting
                    # BUG FIX: Track send success - only update state if send succeeded
                    report_sent = False
                    if "6h" in report_types:
                        blocks_found = state.earnings.get("lifetime_blocks", 0)
                        first_block_t = state.block_history[0]["t"] if state.block_history else None
                        uptime_data = {n: state.get_uptime(n) for n in miner_status}
                        hashrate_trend = state.get_hashrate_trend()
                        personal_bests_summary = state.get_personal_bests_summary()
                        # Get infrastructure health for report (if metrics enabled)
                        infra_health_for_report = get_infrastructure_health() if CONFIG.get("metrics_enabled", True) else None
                        # Daily summary for night report only (last report before quiet hours)
                        night_summary = state.get_daily_summary() if _is_final_report_now() else None
                        # Pass diff_trends to include difficulty/hashrate trends in the 6h report
                        embed = create_report_embed(net_phs, fleet_ths, odds, md, diff, temps, prices, bri, trends, power, extremes, wb, health_scores, blocks_found, None, uptime_data, hashrate_trend, personal_bests_summary, coin_symbol=primary_coin, diff_trends=diff_trends, infra_health=infra_health_for_report, daily_summary=night_summary, first_block_time=first_block_t, aux_diff_trends=aux_diff_trends)
                        report_sent = send_alert("6h_report", embed, state)
                    elif "weekly" in report_types:
                        uptime = {n: state.get_uptime(n) for n in miner_status}
                        embed = create_weekly_embed(state.get_weekly_summary(), uptime, health_scores, state.get_weekly_earnings(prices))
                        report_sent = send_alert("weekly_report", embed, state)
                    elif "monthly" in report_types:
                        report_sent = send_alert("monthly_earnings", create_monthly_earnings_embed(state.get_monthly_earnings(prices), prices, wb), state)
                    elif "quarterly" in report_types:
                        trends_30d = {"difficulty": diff_trends.get("30d"), "network_phs": net_trends.get("30d")}
                        embed = create_quarterly_embed(state.get_quarterly_summary(), trends_30d, prices, wb)
                        report_sent = send_alert("quarterly_report", embed, state)
                        if report_sent:
                            logger.info(f"Quarterly report sent: {qn}")
                    elif "special" in report_types:
                        current_stats = {"network_phs": net_phs, "fleet_ths": fleet_ths, "daily_odds": odds["daily_odds_pct"]}
                        embed = create_special_date_embed(special_info, current_stats, net_trends)
                        report_sent = send_alert("special_date", embed, state)
                        if report_sent:
                            logger.info(f"Special date report: {special_info['name']}")

                    # If send failed, don't update state - retry next cycle
                    if not report_sent:
                        logger.warning(f"Report send failed, will retry next cycle: {report_types}")
                        # Don't update state, so we retry on next loop iteration
                    else:
                        # Update state for successfully sent reports
                        if is_6h_due:
                            state.last_report_hour = report_hour_key
                            if REPORT_FREQUENCY == "daily":
                                state.last_daily_report = now.strftime("%Y-%m-%d")
                        if is_weekly_due:
                            state.reset_weekly()
                            state.last_weekly_report = wn
                        if is_monthly_due:
                            state.reset_monthly()
                            state.last_monthly_report = mn
                        if is_quarterly_due:
                            state.reset_quarterly()
                            state.last_quarterly_report = qn
                        if is_special_due:
                            state.last_special_date = date_key

            # History is saved immediately after recording in record_sample block above

            # Check for updates every 6 hours
            if UPDATE_CHECK_ENABLED and AUTO_UPDATE_MODE != "disabled" and \
                    (time.time() - last_update_check) > UPDATE_CHECK_INTERVAL:
                last_update_check = time.time()
                update_info = check_for_updates()
                if update_info and update_info.get("update_available"):
                    latest_ver = update_info.get("latest_version", "")
                    if latest_ver and latest_ver != last_notified_version:
                        logger.info(f"Update available: v{latest_ver} (mode={AUTO_UPDATE_MODE})")
                        if AUTO_UPDATE_MODE == "auto":
                            # Apply update immediately — notify before + after
                            success = perform_auto_update(update_info)
                            if success:
                                last_notified_version = latest_ver
                        else:
                            # notify mode — send alert, let operator decide
                            sent = send_alert("update_available", create_update_embed(update_info), state)
                            if sent:
                                last_notified_version = latest_ver

            # ═══════════════════════════════════════════════════════════════════════════════
            # ALERT BATCH FLUSH - Send any batched alerts if window has expired
            # ═══════════════════════════════════════════════════════════════════════════════
            # This checks if the batch window (default 5 min) has passed since the oldest
            # queued alert, and if so, flushes all batched alerts as digest notifications.
            if ALERT_BATCHING_ENABLED and state.should_flush_batch():
                state.flush_alert_batch()

            state.save()
            lv, _ = get_status_level(net_phs, primary_coin)
            logger.info(f"[{now.strftime('%H:%M:%S')}] Net: {net_phs:.2f} PH/s | Fleet: {fleet_ths:.2f} TH/s | Odds: {odds['daily_odds_pct']:.1f}% | {lv}")

        except Exception as e:
            import traceback
            logger.error(f"Error: {e}")
            logger.error(traceback.format_exc())

        # Clear first report cycle flag after first iteration
        if first_report_cycle:
            first_report_cycle = False
            logger.debug("First report cycle complete - catch-up reporting now enabled")

        # P1 AUDIT FIX: Use coin-aware check interval for fast-block coins
        # This ensures we check more frequently for DGB (30s), DOGE (45s), etc.
        current_interval = get_check_interval(primary_coin)
        time.sleep(current_interval)

# === CLI ===
def quick_status():
    primary_coin = get_primary_coin()
    logger.info(f"Status check ({primary_coin})...")
    net = fetch_network_stats(primary_coin)
    fleet_ths, md, temps, status, power, uptimes, mblocks, mpools, mstats, _, _, _, _ = get_total_hashrate()
    prices = fetch_coin_price(primary_coin)
    if net:
        odds = calc_odds(net["network_phs"], fleet_ths, primary_coin)
        lv, em = get_status_level(net["network_phs"], primary_coin)
        logger.info(f"Network: {net['network_phs']:.2f} PH/s {em} {lv}")
        logger.info(f"Fleet: {fleet_ths:.2f} TH/s | Odds: {odds['daily_odds_pct']:.1f}%")

        # Sort miners by IP address for consistent ordering
        def sort_key_ip(item):
            name = item[0]
            ip_match = re.search(r'(\d+)\.(\d+)\.(\d+)\.(\d+)', name)
            if ip_match:
                return tuple(int(x) for x in ip_match.groups())
            return (999, 999, 999, name)

        sorted_md = sorted(md.items(), key=sort_key_ip)

        for n, hr in sorted_md:
            st = "🎰" if status.get(n) == "pool_only" else ("🟢" if status.get(n) != "offline" else "🔴")
            # Temperature with thermal emoji: 🔥 hot (>=80°C), 🌡️ warm (>=70°C), ❄️ cool (<70°C)
            t = ""
            if temps.get(n):
                chip_temp = temps[n].get('chip') or temps[n].get('board') or temps[n].get('temp') or 0
                if chip_temp > 0:
                    if chip_temp >= 80:
                        t = f" 🔥{chip_temp:.0f}°"
                    elif chip_temp >= 70:
                        t = f" 🌡️{chip_temp:.0f}°"
                    else:
                        t = f" ❄️{chip_temp:.0f}°"
            shares = mstats.get(n, {})
            logger.info(f"  {st} {n}: {hr}{t} [{shares.get('accepted',0)} acc]")
        if prices:
            cur = get_currency_meta()
            fiat_val = prices.get(cur["code"], prices.get("usd", 0))
            sats = prices.get("sats", 0)
            sats_str = f" | {sats} sats" if sats else ""
            logger.info(f"{cur['symbol']}{fiat_val:.6f} {REPORT_CURRENCY}{sats_str}")

def show_help():
    # CLI help uses print for direct user output
    help_text = f"""
Spiral Sentinel v{__version__}
Autonomous Solo Mining Monitor
CONVERGENT SPIRAL EDITION

Supported: SHA-256d (DGB, BTC, BCH, BC2, NMC, SYS, XMY, FBTC)
           Scrypt   (LTC, DOGE, PEP, CAT)

SELF-HEALING FEATURES
  Auto-Restart     Miners offline 20+ min auto-restarted
  Zombie Detection Miners not submitting valid shares restarted
  Thermal Guard    Alerts on warning (75C) / critical (85C)
  Blip Detection   Catches brief power outages
  Health Scoring   0-100 rating per miner

DEVICE SUPPORT
  HTTP API miners  BitAxe, NMaxe, NerdQAxe, Antminer, Whatsminer, etc.
                   Polled directly via HTTP/CGMiner for hashrate & temps
  Stratum-only     ESP32 Miner V2 (no HTTP API)
                   Shown as 🎰 Online (Stratum) — monitored via pool connections
                   Add manually: dashboard setup or spiralpool-scan

REPORTING
  6-Hour Reports   Network stats, fleet status, odds
  Weekly Summary   Uptime, blocks found, averages
  Monthly Earnings Total coins mined with fiat value

COMMANDS
  python3 SpiralSentinel.py             Start monitoring
  python3 SpiralSentinel.py --status    Quick status check
  python3 SpiralSentinel.py --test      Test Discord webhook
  python3 SpiralSentinel.py --reload    Trigger miner reload (for running instance)
  python3 SpiralSentinel.py --reset     Reboot ALL miners in fleet (interactive)
  python3 SpiralSentinel.py --help      This message

Config: ~/.spiralsentinel/config.json
Miners: /spiralpool/data/miners.json
"""
    logger.info(help_text)

def test_notification():
    logger.info("Testing Discord webhook...")
    embed = _embed("TEST", f"Spiral Sentinel v{__version__} OK!", COLORS["green"], [{"name": "Status", "value": "OK", "inline": True}])
    result = send_discord(embed)
    logger.info(f"Discord test result: {result}")

def trigger_reload():
    """Create the reload trigger file to signal running Sentinel to reload miners."""
    try:
        SHARED_DATA_DIR.mkdir(parents=True, exist_ok=True)
        MINER_RELOAD_TRIGGER.touch()
        logger.info(f"Created reload trigger: {MINER_RELOAD_TRIGGER}")
        logger.info("Running Sentinel instance will reload miners on next check cycle.")

        # Also show current miner count
        miners = load_miners()
        total = sum(len(v) for v in miners.values())
        logger.info(f"Current miner database has {total} miners configured.")
        for mtype, mlist in miners.items():
            if mlist:
                logger.info(f"  {mtype}: {len(mlist)} miners")
    except Exception as e:
        logger.error(f"Failed to create reload trigger: {e}")


def fleet_reset():
    """Send reboot/restart command to ALL miners in the fleet.

    This is a CLI command for manual fleet-wide restart operations.
    Supports all miner types: AxeOS, Avalon, Antminer, Whatsminer, Goldshell, etc.
    """
    logger.info("=" * 60)
    logger.info("FLEET RESET - Sending reboot command to all miners")
    logger.info("=" * 60)

    # Load all configured miners
    miners = load_miners()
    total = sum(len(v) for v in miners.values())

    if total == 0:
        logger.warning("No miners configured in database!")
        logger.info(f"Add miners to: {MINER_DB_FILE}")
        return

    logger.info(f"Found {total} miners in fleet:")
    for mtype, mlist in miners.items():
        if mlist:
            logger.info(f"  {mtype}: {len(mlist)} miners")

    # Confirmation prompt
    logger.info("")
    logger.info("⚠️  WARNING: This will reboot ALL miners in the fleet!")
    logger.info("Press Enter to continue or Ctrl+C to cancel...")
    try:
        input()
    except KeyboardInterrupt:
        logger.info("\nCancelled.")
        return

    logger.info("")
    logger.info("Starting fleet reset...")

    # Track results
    success_count = 0
    fail_count = 0
    results = []

    # Iterate through all miner types
    for miner_type, miner_list in miners.items():
        for miner in miner_list:
            ip = miner.get("ip", "")
            name = miner.get("name", ip)
            port = miner.get("port", 4028)

            if not ip:
                continue

            logger.info(f"  Rebooting {name} ({miner_type}) at {ip}...")

            try:
                if restart_miner(miner_type, ip, port):
                    logger.info(f"    ✓ {name} - reboot command sent")
                    success_count += 1
                    results.append({"name": name, "ip": ip, "type": miner_type, "status": "success"})
                else:
                    logger.warning(f"    ✗ {name} - reboot failed (API not supported or unreachable)")
                    fail_count += 1
                    results.append({"name": name, "ip": ip, "type": miner_type, "status": "failed"})
            except Exception as e:
                logger.error(f"    ✗ {name} - error: {e}")
                fail_count += 1
                results.append({"name": name, "ip": ip, "type": miner_type, "status": "error", "error": str(e)})

            # Small delay between requests to avoid overwhelming network
            time.sleep(0.5)

    # Summary
    logger.info("")
    logger.info("=" * 60)
    logger.info("FLEET RESET COMPLETE")
    logger.info("=" * 60)
    logger.info(f"  ✓ Success: {success_count}")
    logger.info(f"  ✗ Failed:  {fail_count}")
    logger.info(f"  Total:     {total}")

    if fail_count > 0:
        logger.info("")
        logger.info("Failed miners:")
        for r in results:
            if r["status"] != "success":
                err = r.get("error", "API not supported or unreachable")
                logger.info(f"  - {r['name']} ({r['type']}) at {r['ip']}: {err}")

    # Send Discord notification if webhook configured
    if DISCORD_WEBHOOK_URL:
        try:
            fields = [
                {"name": "✓ Success", "value": str(success_count), "inline": True},
                {"name": "✗ Failed", "value": str(fail_count), "inline": True},
                {"name": "Total", "value": str(total), "inline": True},
            ]
            if fail_count > 0:
                failed_list = "\n".join([f"• {r['name']} ({r['type']})" for r in results if r["status"] != "success"][:10])
                if len([r for r in results if r["status"] != "success"]) > 10:
                    failed_list += f"\n... and {len([r for r in results if r['status'] != 'success']) - 10} more"
                fields.append({"name": "Failed Miners", "value": failed_list, "inline": False})

            color = COLORS["green"] if fail_count == 0 else COLORS["yellow"] if success_count > 0 else COLORS["red"]
            embed = _embed("🔄 FLEET RESET COMPLETE", f"Manual fleet reboot initiated via CLI", color, fields,
                          footer=f"🌀 Spiral Sentinel v{__version__}")
            send_discord(embed)
            logger.info("Discord notification sent.")
        except Exception as e:
            logger.warning(f"Failed to send Discord notification: {e}")

if __name__ == "__main__":
    args = sys.argv[1:] if len(sys.argv) > 1 else []
    if "--help" in args or "-h" in args: show_help()
    elif "--status" in args or "-s" in args: quick_status()
    elif "--test" in args or "-t" in args: test_notification()
    elif "--reload" in args or "-r" in args: trigger_reload()
    elif "--reset" in args: fleet_reset()
    else:
        state = MonitorState()

        # ═══════════════════════════════════════════════════════════════════════════════
        # GRACEFUL SHUTDOWN HANDLER - Ensures state is saved on termination
        # ═══════════════════════════════════════════════════════════════════════════════
        # Handles SIGTERM (systemd stop), SIGINT (Ctrl+C), and SIGHUP (terminal hangup)
        # Flushes any pending batched alerts before shutdown to prevent alert loss
        _shutdown_requested = False

        def graceful_shutdown(signum, frame):
            """Handle shutdown signals gracefully."""
            global _shutdown_requested
            if _shutdown_requested:
                # Already shutting down, force exit on second signal
                logger.warning("Forced shutdown requested")
                sys.exit(1)
            _shutdown_requested = True

            sig_name = {signal.SIGTERM: "SIGTERM", signal.SIGINT: "SIGINT"}.get(signum, str(signum))
            if hasattr(signal, 'SIGHUP') and signum == signal.SIGHUP:
                sig_name = "SIGHUP"

            logger.info(f"Received {sig_name}, initiating graceful shutdown...")

            try:
                # Flush any pending batched alerts
                if hasattr(state, 'global_alert_batch') and state.global_alert_batch:
                    logger.info(f"Flushing {len(state.global_alert_batch)} pending batched alerts...")
                    state.flush_alert_batch()

                # Save state to disk
                logger.info("Saving state...")
                state.save()
                logger.info("Shutdown complete")
            except Exception as e:
                logger.error(f"Error during shutdown: {e}")

            sys.exit(0)

        # Register signal handlers
        signal.signal(signal.SIGTERM, graceful_shutdown)
        signal.signal(signal.SIGINT, graceful_shutdown)
        # SIGHUP not available on Windows
        if hasattr(signal, 'SIGHUP'):
            signal.signal(signal.SIGHUP, graceful_shutdown)

        try:
            monitor_loop(state)
        except KeyboardInterrupt:
            # Fallback for Ctrl+C if signal handler didn't catch it
            logger.info("Shutting down...")
            state.save()
