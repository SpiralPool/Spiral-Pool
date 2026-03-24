# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""
Spiral Pool Dashboard - Cyberpunk Mining Monitor

A sleek dashboard for monitoring mining hardware including:
- BitAxe, NerdQAxe++, AxeOS-based miners (HTTP API)
- Avalon ASICs (CGMiner API)
- Bitmain Antminer S19/S21/T21 series (CGMiner API)
- MicroBT Whatsminer M30/M50/M60 series (CGMiner API)
- Innosilicon A-series (CGMiner API)

Includes Pool API and Prometheus metrics integration.

ASIC Miner API Protocol References (protocol documentation, not derived code):
- CGMiner API protocol: https://github.com/ckolivas/cgminer/blob/master/API-README
- Whatsminer API: https://www.whatsminer.com/file/WhatsminerAPI%20V2.0.3.pdf

See LICENSE file for full BSD-3-Clause license terms.
"""

__version__ = "1.2.1-CONVERGENT_SPIRAL"

import os
import json
import time
import socket
import asyncio
import threading
import ipaddress
import concurrent.futures
import re
import hashlib
import hmac
import logging
from datetime import datetime, timedelta, timezone
from pathlib import Path
from collections import deque
from functools import wraps
from flask import Flask, render_template, jsonify, request, redirect, url_for, session, g, send_from_directory
from flask_socketio import SocketIO, emit
from flask_login import LoginManager, UserMixin, login_user, logout_user, login_required, current_user
from werkzeug.exceptions import BadRequest
from urllib.parse import urlparse, urljoin
import argparse
import copy
import sys
import signal
import tempfile
import shutil
import requests
import secrets
import yaml

try:
    import bcrypt
    BCRYPT_AVAILABLE = True
except ImportError:
    BCRYPT_AVAILABLE = False

app = Flask(__name__)

# Session secret key - persist across restarts for session continuity
# Store in the same data directory as auth config
SECRET_KEY_FILE = Path("/spiralpool/dashboard/data/secret_key")
def get_or_create_secret_key():
    """Get existing secret key or create new one. Persists across restarts."""
    try:
        SECRET_KEY_FILE.parent.mkdir(parents=True, exist_ok=True)
        if SECRET_KEY_FILE.exists():
            key = SECRET_KEY_FILE.read_text().strip()
            if len(key) >= 32:
                return key
        # Generate new key and save it
        key = secrets.token_hex(32)
        SECRET_KEY_FILE.write_text(key)
        SECRET_KEY_FILE.chmod(0o600)  # Restrict permissions
        return key
    except Exception:
        # Fallback to in-memory key if file operations fail
        return secrets.token_hex(32)

app.secret_key = get_or_create_secret_key()

# WebSocket support for real-time updates
# SECURITY: Restrict CORS to same-origin connections only
# For production, set DASHBOARD_CORS_ORIGINS environment variable to allowed origins
# Example: DASHBOARD_CORS_ORIGINS="http://localhost:1618,https://pool.example.com"
_cors_origins = os.environ.get("DASHBOARD_CORS_ORIGINS", "").strip()
if _cors_origins:
    _allowed_origins = [origin.strip() for origin in _cors_origins.split(",") if origin.strip()]
else:
    # Default: only allow same-origin (no cross-origin requests)
    _allowed_origins = []
socketio = SocketIO(app, cors_allowed_origins=_allowed_origins if _allowed_origins else None, async_mode='threading')


# ═══════════════════════════════════════════════════════════════════════════════
# SECURITY HEADERS
# ═══════════════════════════════════════════════════════════════════════════════

@app.after_request
def add_security_headers(response):
    """Add security headers to all HTTP responses."""
    response.headers['X-Content-Type-Options'] = 'nosniff'
    response.headers['X-Frame-Options'] = 'DENY'
    response.headers['Referrer-Policy'] = 'strict-origin-when-cross-origin'
    response.headers['X-XSS-Protection'] = '1; mode=block'
    response.headers['Permissions-Policy'] = 'camera=(), microphone=(), geolocation=()'
    return response


# ═══════════════════════════════════════════════════════════════════════════════
# AUTHENTICATION SYSTEM (H-01 Security Fix)
# ═══════════════════════════════════════════════════════════════════════════════

# Flask-Login setup
login_manager = LoginManager()
login_manager.init_app(app)
login_manager.login_view = 'login'
login_manager.login_message = 'Please log in to access this page.'
login_manager.login_message_category = 'warning'

# Authentication configuration from environment
# DASHBOARD_AUTH_ENABLED: Set to "false" to disable auth (NOT RECOMMENDED for production)
# DASHBOARD_ADMIN_PASSWORD: Admin password (required if auth enabled)
# DASHBOARD_API_KEY: API key for programmatic access (optional)
# DASHBOARD_SESSION_LIFETIME: Session timeout in hours (default: 24)
AUTH_ENABLED = os.environ.get("DASHBOARD_AUTH_ENABLED", "true").lower() != "false"
ADMIN_PASSWORD = os.environ.get("DASHBOARD_ADMIN_PASSWORD", "").strip()
API_KEY = os.environ.get("DASHBOARD_API_KEY", "").strip()
try:
    SESSION_LIFETIME_HOURS = int(os.environ.get("DASHBOARD_SESSION_LIFETIME", "24"))
except (ValueError, TypeError):
    SESSION_LIFETIME_HOURS = 24

# Auth file path for persistent password hash
AUTH_FILE = Path("/spiralpool/dashboard/data/auth.json")

# Simple user class for Flask-Login
class AdminUser(UserMixin):
    def __init__(self, user_id="admin"):
        self.id = user_id
        self.username = "admin"

    def get_id(self):
        return self.id


@login_manager.user_loader
def load_user(user_id):
    """Load user by ID - only admin user supported."""
    if user_id == "admin":
        return AdminUser("admin")
    return None


def hash_password(password: str) -> str:
    """Hash password using bcrypt if available, fallback to SHA-256."""
    if BCRYPT_AVAILABLE:
        return bcrypt.hashpw(password.encode('utf-8'), bcrypt.gensalt()).decode('utf-8')
    else:
        # Fallback: SHA-256 with salt (less secure but functional)
        salt = secrets.token_hex(16)
        hashed = hashlib.sha256((salt + password).encode('utf-8')).hexdigest()
        return f"sha256:{salt}:{hashed}"


def verify_password(password: str, password_hash: str) -> bool:
    """Verify password against hash using constant-time comparison."""
    if not password or not password_hash:
        return False

    if BCRYPT_AVAILABLE and not password_hash.startswith("sha256:"):
        try:
            return bcrypt.checkpw(password.encode('utf-8'), password_hash.encode('utf-8'))
        except Exception:
            return False
    elif password_hash.startswith("sha256:"):
        # Fallback verification
        parts = password_hash.split(":")
        if len(parts) != 3:
            return False
        _, salt, stored_hash = parts
        computed_hash = hashlib.sha256((salt + password).encode('utf-8')).hexdigest()
        return hmac.compare_digest(computed_hash, stored_hash)

    return False


def load_auth_config() -> dict:
    """Load authentication configuration from file."""
    if AUTH_FILE.exists():
        try:
            with open(AUTH_FILE, 'r') as f:
                return json.load(f)
        except Exception as e:
            app.logger.error(f"Failed to load auth config: {e}")
    return {}


def save_auth_config(config: dict):
    """Save authentication configuration to file.
    Uses atomic write (temp file + fsync + rename) to prevent corruption
    if power fails mid-write. Without this, a partial write could leave
    an empty or truncated auth.json, locking the admin out of the dashboard."""
    try:
        AUTH_FILE.parent.mkdir(parents=True, exist_ok=True)
        _atomic_json_save(str(AUTH_FILE), config, indent=2)
        # Restrict file permissions (owner read/write only)
        os.chmod(AUTH_FILE, 0o600)
    except Exception as e:
        app.logger.error(f"Failed to save auth config: {e}")


def is_first_time_setup() -> bool:
    """Check if this is first-time setup (no password configured)."""
    if ADMIN_PASSWORD:
        return False
    auth_config = load_auth_config()
    return not auth_config.get("password_hash")


def validate_api_key(key: str) -> bool:
    """Validate API key for programmatic access."""
    if not API_KEY:
        return False
    if not key:
        return False
    return hmac.compare_digest(key, API_KEY)


def api_key_or_login_required(f):
    """Decorator: Require either API key or login session."""
    @wraps(f)
    def decorated_function(*args, **kwargs):
        # SECURITY (F-03): AUTH_ENABLED=false is only honored for loopback connections.
        # On external interfaces auth is always enforced, regardless of this setting,
        # to prevent accidental public exposure if the env var is misconfigured.
        if not AUTH_ENABLED:
            client_ip = request.remote_addr or ''
            if client_ip in ('127.0.0.1', '::1'):
                return f(*args, **kwargs)
            # Non-localhost: fall through to normal auth checks

        # Check API key in header only (SECURITY: Never accept from query params)
        # Query params are logged in server logs, browser history, and referrer headers
        api_key = request.headers.get('X-API-Key')
        if api_key and validate_api_key(api_key):
            return f(*args, **kwargs)

        # SECURITY: Warn if API key was passed in query params (legacy/insecure)
        if request.args.get('api_key'):
            app.logger.warning(f"SECURITY: Rejected API key in query params from {request.remote_addr} - use X-API-Key header instead")

        # Then check session login
        if current_user.is_authenticated:
            return f(*args, **kwargs)

        # Not authenticated
        if request.is_json or request.path.startswith('/api/'):
            return jsonify({"error": "Unauthorized", "message": "Authentication required"}), 401
        return redirect(url_for('login', next=request.url))

    return decorated_function


def admin_required(f):
    """Decorator: Require admin login (API key not sufficient for some operations).

    SECURITY: Also enforces CSRF protection for state-changing requests by
    validating Origin/Referer header matches the server's host.
    """
    @wraps(f)
    def decorated_function(*args, **kwargs):
        # SECURITY (F-03): AUTH_ENABLED=false is only honored for loopback connections.
        if not AUTH_ENABLED:
            client_ip = request.remote_addr or ''
            if client_ip in ('127.0.0.1', '::1'):
                return f(*args, **kwargs)
            # Non-localhost: fall through to normal auth checks

        # Only session login accepted for admin operations
        if not current_user.is_authenticated:
            if request.is_json or request.path.startswith('/api/'):
                return jsonify({"error": "Unauthorized", "message": "Admin login required"}), 401
            return redirect(url_for('login', next=request.url))

        # SECURITY: CSRF protection for state-changing requests
        # Verify Origin or Referer header matches our host
        if request.method in ('POST', 'PUT', 'DELETE', 'PATCH'):
            origin = request.headers.get('Origin')
            referer = request.headers.get('Referer')

            # Get expected host from request
            expected_host = request.host

            # Check Origin header first (preferred)
            if origin:
                try:
                    origin_parsed = urlparse(origin)
                    origin_host = origin_parsed.netloc
                    if origin_host != expected_host:
                        app.logger.warning(f"CSRF: Origin mismatch - got {origin_host}, expected {expected_host}")
                        return jsonify({"error": "Forbidden", "message": "CSRF validation failed"}), 403
                except Exception:
                    return jsonify({"error": "Forbidden", "message": "Invalid Origin header"}), 403
            elif referer:
                # Fall back to Referer if no Origin
                try:
                    referer_parsed = urlparse(referer)
                    referer_host = referer_parsed.netloc
                    if referer_host != expected_host:
                        app.logger.warning(f"CSRF: Referer mismatch - got {referer_host}, expected {expected_host}")
                        return jsonify({"error": "Forbidden", "message": "CSRF validation failed"}), 403
                except Exception:
                    return jsonify({"error": "Forbidden", "message": "Invalid Referer header"}), 403
            else:
                # No Origin or Referer - reject for browser requests
                # Allow if this looks like a non-browser request (curl, scripts, etc.)
                user_agent = request.headers.get('User-Agent', '').lower()
                if 'mozilla' in user_agent or 'chrome' in user_agent or 'safari' in user_agent:
                    app.logger.warning(f"CSRF: Missing Origin/Referer from browser request to {request.path}")
                    return jsonify({"error": "Forbidden", "message": "CSRF validation failed - missing headers"}), 403
                # Non-browser requests allowed through (they can't use session cookies anyway)

        return f(*args, **kwargs)

    return decorated_function


# Session configuration
app.config['PERMANENT_SESSION_LIFETIME'] = timedelta(hours=SESSION_LIFETIME_HOURS)
# SECURITY: Secure cookies require HTTPS. Default to false for local network deployments.
# Set DASHBOARD_SECURE_COOKIES=true if running behind HTTPS reverse proxy.
app.config['SESSION_COOKIE_SECURE'] = os.environ.get("DASHBOARD_SECURE_COOKIES", "false").lower() == "true"
app.config['SESSION_COOKIE_HTTPONLY'] = True
# SECURITY: Use 'Strict' to ensure session cookies are sent on same-origin POST requests via fetch/XHR.
# 'Lax' can cause issues where browsers don't include cookies on JavaScript-initiated POST requests.
app.config['SESSION_COOKIE_SAMESITE'] = 'Strict'


# ─────────────────────────────────────────────
# ERROR HANDLERS - Return JSON for API endpoints
# ─────────────────────────────────────────────

@app.errorhandler(Exception)
def handle_exception(e):
    """Handle uncaught exceptions - return JSON for API routes."""
    if request.path.startswith('/api/'):
        app.logger.error(f"Unhandled exception on {request.path}: {str(e)}")
        return jsonify({"success": False, "error": "Internal server error"}), 500
    # For non-API routes, re-raise to use default Flask handling
    raise e

@app.errorhandler(404)
def handle_404(e):
    """Handle 404 errors - return JSON for API routes."""
    if request.path.startswith('/api/'):
        return jsonify({"success": False, "error": "Endpoint not found"}), 404
    return "<h1>404 - Page not found</h1><p><a href='/'>Return to dashboard</a></p>", 404

@app.errorhandler(500)
def handle_500(e):
    """Handle 500 errors - return JSON for API routes."""
    if request.path.startswith('/api/'):
        return jsonify({"success": False, "error": "Internal server error"}), 500
    return "<h1>500 - Internal server error</h1><p><a href='/'>Return to dashboard</a></p>", 500


def is_safe_redirect_url(target: str) -> bool:
    """Validate redirect URL to prevent open redirect attacks.

    Only allows redirects to the same host to prevent attackers from
    redirecting users to malicious sites after login.
    """
    if not target:
        return False
    ref_url = urlparse(request.host_url)
    test_url = urlparse(urljoin(request.host_url, target))
    return test_url.scheme in ('http', 'https') and ref_url.netloc == test_url.netloc


def get_safe_redirect_url() -> str:
    """Get safe redirect URL from request args, defaulting to index."""
    next_page = request.args.get('next')
    if next_page and is_safe_redirect_url(next_page):
        return next_page
    return url_for('index')


# Track failed login attempts for rate limiting
_login_attempts = {}
_login_attempts_lock = threading.Lock()
LOGIN_RATE_LIMIT_WINDOW = 300  # 5 minutes
LOGIN_MAX_ATTEMPTS = 5

def check_login_rate_limit(ip: str) -> tuple:
    """Check if IP is rate limited for login attempts.

    Returns:
        tuple: (allowed: bool, remaining_seconds: int)
            - allowed: True if login attempt is allowed
            - remaining_seconds: Seconds remaining until lockout expires (0 if not locked)
    """
    now = time.time()
    with _login_attempts_lock:
        # Prune stale entries to prevent unbounded growth from bot scans
        if len(_login_attempts) > 1000:
            stale = [k for k, (_, t) in _login_attempts.items() if now - t > LOGIN_RATE_LIMIT_WINDOW]
            for k in stale:
                del _login_attempts[k]
        if ip in _login_attempts:
            attempts, first_attempt = _login_attempts[ip]
            elapsed = now - first_attempt
            if elapsed > LOGIN_RATE_LIMIT_WINDOW:
                # Window expired, reset
                _login_attempts[ip] = (0, now)
                return (True, 0)
            if attempts >= LOGIN_MAX_ATTEMPTS:
                remaining = int(LOGIN_RATE_LIMIT_WINDOW - elapsed)
                return (False, remaining)
    return (True, 0)


def record_login_attempt(ip: str, success: bool):
    """Record a login attempt for rate limiting."""
    now = time.time()
    with _login_attempts_lock:
        if success:
            # Clear on successful login
            _login_attempts.pop(ip, None)
            return

        if ip in _login_attempts:
            attempts, first_attempt = _login_attempts[ip]
            if now - first_attempt > LOGIN_RATE_LIMIT_WINDOW:
                _login_attempts[ip] = (1, now)
            else:
                _login_attempts[ip] = (attempts + 1, first_attempt)
        else:
            _login_attempts[ip] = (1, now)


# Thread locks for cache synchronization (prevent race conditions)
_block_reward_lock = threading.Lock()
_miner_cache_lock = threading.Lock()
_pool_stats_lock = threading.Lock()
_lifetime_stats_lock = threading.Lock()
_share_heatmap_lock = threading.Lock()

# HTTP Session with connection pooling for better performance
# Reuses TCP connections instead of creating new ones for each request
_http_session = requests.Session()
_http_session.headers.update({
    "User-Agent": "SpiralPool-Dashboard/1.0",
    "Accept": "application/json"
})
# Configure connection pool: 10 connections max, 5 per host
_adapter = requests.adapters.HTTPAdapter(pool_connections=10, pool_maxsize=10, max_retries=3)
_http_session.mount("http://", _adapter)
_http_session.mount("https://", _adapter)


# ═══════════════════════════════════════════════════════════════════════════════
# REQUEST TIMEOUT FALLBACK STRATEGY - Consistent timeout handling across dashboard
# ═══════════════════════════════════════════════════════════════════════════════

# Default timeout tiers (in seconds)
TIMEOUT_FAST = 2       # Quick status checks
TIMEOUT_NORMAL = 5     # Standard API calls
TIMEOUT_SLOW = 10      # Slower external APIs
TIMEOUT_LONG = 30      # Long operations (block explorer, etc.)

# Timeout multipliers for retries
TIMEOUT_RETRY_MULTIPLIER = 1.5

def robust_request(method: str, url: str, timeout: float = TIMEOUT_NORMAL,
                   max_retries: int = 2, fallback_value=None, **kwargs) -> tuple:
    """
    Make an HTTP request with consistent timeout handling and retry logic.

    Features:
    - Automatic retry with exponential backoff
    - Increasing timeouts on retry
    - Returns fallback value on failure instead of raising
    - Consistent error logging

    Args:
        method: HTTP method (get, post, put, delete)
        url: Target URL
        timeout: Initial timeout in seconds
        max_retries: Number of retry attempts (0 = no retries)
        fallback_value: Value to return on complete failure
        **kwargs: Additional arguments passed to requests

    Returns:
        tuple: (response_json or fallback_value, success: bool, error_msg: str or None)
    """
    last_error = None
    current_timeout = timeout

    for attempt in range(max_retries + 1):
        try:
            method_func = getattr(_http_session, method.lower())
            response = method_func(url, timeout=current_timeout, **kwargs)

            # Success
            if response.status_code == 200:
                try:
                    return (response.json(), True, None)
                except ValueError:
                    return (response.text, True, None)

            # Non-200 but got a response
            return (fallback_value, False, f"HTTP {response.status_code}")

        except requests.exceptions.Timeout as e:
            last_error = f"Timeout after {current_timeout}s"
            # Increase timeout for next retry
            current_timeout = min(current_timeout * TIMEOUT_RETRY_MULTIPLIER, TIMEOUT_LONG)

        except requests.exceptions.ConnectionError as e:
            last_error = "Connection failed"
            # Brief delay before retry
            if attempt < max_retries:
                time.sleep(0.5 * (attempt + 1))

        except requests.exceptions.RequestException as e:
            last_error = str(e)[:100]  # Truncate for safety

        except Exception as e:
            last_error = f"Unexpected: {type(e).__name__}"
            break  # Don't retry unknown errors

    # All retries exhausted
    return (fallback_value, False, last_error)

# Configuration paths - cross-platform support
if os.name == 'nt':
    # Windows: use %LOCALAPPDATA%\SpiralPool\dashboard or fallback to %APPDATA%
    _base_dir = os.environ.get("LOCALAPPDATA") or os.environ.get("APPDATA") or os.path.expanduser("~")
    CONFIG_DIR = Path(_base_dir) / "SpiralPool" / "dashboard" / "data"
else:
    # Linux/Unix: use /spiralpool/ install directory
    CONFIG_DIR = Path("/spiralpool/dashboard/data")
CONFIG_FILE = CONFIG_DIR / "dashboard_config.json"
STATS_FILE = CONFIG_DIR / "dashboard_stats.json"

# ═══════════════════════════════════════════════════════════════════════════════
# ERROR HANDLING - Security measures to prevent information disclosure (M-10 Fix)
# ═══════════════════════════════════════════════════════════════════════════════

# Error code mappings for safe error responses
_ERROR_CODES = {
    "connection": "E001",
    "timeout": "E002",
    "validation": "E003",
    "internal": "E004",
    "permission": "E005",
    "not_found": "E006",
}


def safe_error_response(error: Exception, error_type: str = "internal", log_full: bool = True) -> dict:
    """
    Create a safe error response that doesn't expose internal details to clients.
    SECURITY (M-10): Returns generic error message, logs full exception.

    Args:
        error: The exception that occurred
        error_type: Type of error (connection, timeout, validation, internal, permission, not_found)
        log_full: Whether to log the full exception (default True)

    Returns:
        Dictionary with safe error message and error code
    """
    error_code = _ERROR_CODES.get(error_type, "E000")

    # Log full error details for debugging
    if log_full:
        app.logger.error(f"Error [{error_code}]: {type(error).__name__}: {error}")

    # Generic error messages for each type
    safe_messages = {
        "connection": "Failed to connect to service",
        "timeout": "Request timed out",
        "validation": "Invalid input data",
        "internal": "An internal error occurred",
        "permission": "Permission denied",
        "not_found": "Resource not found",
    }

    return {
        "success": False,
        "error": safe_messages.get(error_type, "An error occurred"),
        "error_code": error_code
    }


# ═══════════════════════════════════════════════════════════════════════════════
# INPUT VALIDATION - Security measures to prevent injection attacks
# ═══════════════════════════════════════════════════════════════════════════════

# Valid coins whitelist (SHA-256d + Scrypt)
# Must match frontend coinInfo/allCoins/validCoins arrays in setup.html
VALID_COINS = {
    # SHA-256d coins
    'BC2', 'BCH', 'BTC', 'DGB', 'FBTC', 'NMC', 'QBX', 'SYS', 'XMY',
    # Scrypt coins
    'CAT', 'DGB-SCRYPT', 'DOGE', 'LTC', 'PEP'
}

# Wallet address validation patterns
# BTC formats supported:
#   - P2PKH (Legacy): starts with '1', Base58, 26-35 chars (e.g., 1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2)
#   - P2SH (Script): starts with '3', Base58, 26-35 chars (e.g., 3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy)
#   - P2WPKH (Native SegWit): starts with 'bc1q', Bech32, exactly 42 chars
#   - P2WSH (SegWit Script): starts with 'bc1q', Bech32, exactly 62 chars
#   - P2TR (Taproot): starts with 'bc1p', Bech32m, exactly 62 chars
# BC2 (Bitcoin II) uses IDENTICAL address formats to Bitcoin - same patterns
WALLET_PATTERNS = {
    'DGB': re.compile(r'^[DS][a-km-zA-HJ-NP-Z1-9]{25,34}$'),
    'BTC': re.compile(
        r'^(1|3)[a-km-zA-HJ-NP-Z1-9]{25,34}$|'  # P2PKH and P2SH (Legacy/Script)
        r'^bc1q[a-z0-9]{38}$|'                    # P2WPKH (Native SegWit, 42 chars)
        r'^bc1q[a-z0-9]{58}$|'                    # P2WSH (SegWit Script, 62 chars)
        r'^bc1p[a-z0-9]{58}$'                     # P2TR (Taproot, 62 chars)
    ),
    'BCH': re.compile(r'^(bitcoincash:)?[qp][a-z0-9]{41}$|^(1|3)[a-km-zA-HJ-NP-Z1-9]{25,34}$'),
    # BC2 (Bitcoin II) - identical address formats to Bitcoin (bc1q, 1, 3)
    # WARNING: BC2 and BTC addresses are indistinguishable by format alone
    'BC2': re.compile(
        r'^(1|3)[a-km-zA-HJ-NP-Z1-9]{25,34}$|'  # P2PKH and P2SH (Legacy/Script)
        r'^bc1q[a-z0-9]{38}$|'                    # P2WPKH (Native SegWit, 42 chars)
        r'^bc1q[a-z0-9]{58}$|'                    # P2WSH (SegWit Script, 62 chars)
        r'^bc1p[a-z0-9]{58}$'                     # P2TR (Taproot, 62 chars)
    ),
    # === Scrypt Coins ===
    # LTC - Litecoin: L for P2PKH, M or 3 for P2SH, ltc1 for bech32
    'LTC': re.compile(
        r'^L[a-km-zA-HJ-NP-Z1-9]{26,33}$|'        # P2PKH (L prefix)
        r'^[M3][a-km-zA-HJ-NP-Z1-9]{26,33}$|'     # P2SH (M or legacy 3 prefix)
        r'^ltc1q[a-z0-9]{38}$|'                    # P2WPKH (Native SegWit)
        r'^ltc1q[a-z0-9]{58}$'                     # P2WSH (SegWit Script)
    ),
    # DOGE - Dogecoin: D for P2PKH, 9/A for P2SH
    'DOGE': re.compile(r'^D[a-km-zA-HJ-NP-Z1-9]{25,34}$|^[9A][a-km-zA-HJ-NP-Z1-9]{25,34}$'),
    # DGB-SCRYPT - DigiByte Scrypt mode (same address format as DGB)
    'DGB-SCRYPT': re.compile(r'^[DS][a-km-zA-HJ-NP-Z1-9]{25,34}$'),
    # === Additional SHA-256d Coins ===
    # FBTC - Fractal Bitcoin: same format as BTC
    'FBTC': re.compile(
        r'^(1|3)[a-km-zA-HJ-NP-Z1-9]{25,34}$|'
        r'^bc1q[a-z0-9]{38}$|'
        r'^bc1q[a-z0-9]{58}$|'
        r'^bc1p[a-z0-9]{58}$'
    ),
    # QBX - Q-BitX: M prefix P2PKH (0x32), P prefix P2SH (0x37), pq... Dilithium
    'QBX': re.compile(
        r'^(?:'
        r'M[a-km-zA-HJ-NP-Z1-9]{25,34}'   # P2PKH (version byte 0x32 = 'M')
        r'|P[a-km-zA-HJ-NP-Z1-9]{25,34}'  # P2SH (version byte 0x37 = 'P')
        r'|pq[a-zA-Z0-9]{20,80}'           # Post-quantum Dilithium
        r')$'
    ),
    # NMC - Namecoin: N/M prefix for P2PKH, nc1q for bech32
    'NMC': re.compile(r'^[NM][a-km-zA-HJ-NP-Z1-9]{25,34}$|^nc1q[a-z0-9]{38,58}$'),
    # SYS - Syscoin: sys1q for bech32, S for legacy
    'SYS': re.compile(r'^S[a-km-zA-HJ-NP-Z1-9]{25,34}$|^sys1q[a-z0-9]{38,58}$'),
    # XMY - Myriad: M prefix for P2PKH
    'XMY': re.compile(r'^M[a-km-zA-HJ-NP-Z1-9]{25,34}$'),
    # === Additional Scrypt Coins ===
    # PEP - Pepecoin: P prefix
    'PEP': re.compile(r'^P[a-km-zA-HJ-NP-Z1-9]{25,34}$'),
    # CAT - Catcoin: 9 prefix (like DOGE P2SH)
    'CAT': re.compile(r'^9[a-km-zA-HJ-NP-Z1-9]{25,34}$')
}

# Rate limiting for sensitive endpoints
_rate_limit_cache = {}
_rate_limit_lock = threading.Lock()
_api_cache = {}
_api_cache_lock = threading.Lock()
RATE_LIMIT_WINDOW = 60  # seconds
RATE_LIMIT_MAX_REQUESTS = 10  # max requests per window


def validate_wallet_address(coin: str, address: str) -> bool:
    """Validate wallet address format for a given coin."""
    if not address or not isinstance(address, str):
        return False
    if coin not in WALLET_PATTERNS:
        return False
    return bool(WALLET_PATTERNS[coin].match(address.strip()))


def validate_coin_symbol(symbol: str) -> bool:
    """Validate coin symbol against whitelist."""
    return isinstance(symbol, str) and symbol.upper() in VALID_COINS


def validate_miner_ip(ip_str: str) -> bool:
    """
    SECURITY: Validate that an IP address is safe to connect to (SSRF prevention).
    Only allows private network IPs that would be expected for local miners.
    Returns True if IP is valid and safe, False otherwise.
    """
    if not ip_str or not isinstance(ip_str, str):
        return False

    # Strip whitespace and validate format
    ip_str = ip_str.strip()

    # Basic format check - only allow IPv4 for miner connections
    if not re.match(r'^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$', ip_str):
        return False

    try:
        ip = ipaddress.ip_address(ip_str)

        # SECURITY: Only allow private network IPs (RFC 1918)
        # This prevents SSRF attacks to external services like cloud metadata
        if not ip.is_private:
            app.logger.warning(f"SECURITY: Rejecting non-private IP: {ip_str}")
            return False

        # SECURITY: Block localhost to prevent self-attacks
        if ip.is_loopback:
            app.logger.warning(f"SECURITY: Rejecting loopback IP: {ip_str}")
            return False

        # SECURITY: Block link-local addresses (169.254.x.x) - includes cloud metadata
        if ip.is_link_local:
            app.logger.warning(f"SECURITY: Rejecting link-local IP: {ip_str}")
            return False

        # SECURITY: Block multicast and reserved ranges
        if ip.is_multicast or ip.is_reserved:
            app.logger.warning(f"SECURITY: Rejecting multicast/reserved IP: {ip_str}")
            return False

        return True
    except ValueError:
        return False


def check_rate_limit(client_ip: str, endpoint: str) -> bool:
    """Check if client has exceeded rate limit. Returns True if allowed."""
    now = time.time()
    key = f"{client_ip}:{endpoint}"

    with _rate_limit_lock:
        # Prune stale entries to prevent unbounded growth
        if len(_rate_limit_cache) > 1000:
            stale = [k for k, v in _rate_limit_cache.items() if now - v["window_start"] > RATE_LIMIT_WINDOW]
            for k in stale:
                del _rate_limit_cache[k]

        if key not in _rate_limit_cache:
            _rate_limit_cache[key] = {"count": 1, "window_start": now}
            return True

        entry = _rate_limit_cache[key]

        # Reset window if expired
        if now - entry["window_start"] > RATE_LIMIT_WINDOW:
            _rate_limit_cache[key] = {"count": 1, "window_start": now}
            return True

        # Check limit
        if entry["count"] >= RATE_LIMIT_MAX_REQUESTS:
            return False

        entry["count"] += 1
        return True


def sanitize_string(value: str, max_length: int = 256) -> str:
    """Sanitize string input to prevent injection attacks."""
    if not isinstance(value, str):
        return ""
    # Remove null bytes and control characters
    sanitized = re.sub(r'[\x00-\x1f\x7f]', '', value)
    # Truncate to max length
    return sanitized[:max_length].strip()


def csv_safe(value) -> str:
    """Prevent CSV injection by escaping formula-trigger characters.
    When a CSV cell starts with =, +, -, @, tab, or CR, spreadsheet apps
    may interpret it as a formula — prefix with ' to neutralize."""
    s = str(value)
    if s and s[0] in ('=', '+', '-', '@', '\t', '\r'):
        return "'" + s
    return s

# Default configuration
DEFAULT_CONFIG = {
    "dashboard_title": "My Solo Pool",
    "first_run": True,
    "devices": {
        "axeos": [],           # AxeOS/BitAxe devices (BitAxe Ultra, Gamma, etc.)
        "nmaxe": [],           # NMaxe devices
        "nerdqaxe": [],        # NerdQAxe++ devices (~5 TH/s)
        "avalon": [],          # Avalon ASIC devices (Nano 3, etc.)
        "antminer": [],        # Bitmain Antminer S19/S21/T21 series (SHA-256d)
        "antminer_scrypt": [], # Bitmain Antminer L-series (L3+, L7, L9 - Scrypt)
        "whatsminer": [],      # MicroBT Whatsminer M30/M50/M60 series (CGMiner API)
        "innosilicon": [],     # Innosilicon A-series (CGMiner API)
        "goldshell": [],       # Goldshell miners (KD6, LT5, Mini-DOGE, etc.)
        "hammer": [],          # PlebSource Hammer Miner (Scrypt)
        "futurebit": [],       # FutureBit Apollo (SHA-256d)
        "braiins": [],         # BraiinsOS/BOS+ miners (S9, S17, S19, S21 with Braiins firmware)
        "vnish": [],           # Vnish firmware miners (Antminers with Vnish custom firmware)
        "luxos": [],           # LuxOS firmware miners (Antminers with LuxOS firmware)
        "luckyminer": [],      # Lucky Miner LV06/LV07/LV08 (AxeOS-based)
        "jingleminer": [],     # Jingle Miner BTC Solo Pro/Lite (AxeOS-based)
        "zyber": [],           # Zyber 8G/8GP/8S (AxeOS-based, TinyChipHub)
        "gekkoscience": [],    # GekkoScience Compac F, NewPac, R606 (CGMiner)
        "ipollo": [],          # iPollo V1, V1 Mini, G1 (CGMiner + HTTP)
        "ebang": [],           # Ebang/Ebit E9/E10/E11/E12 (CGMiner + HTTP)
        "epic": [],            # ePIC BlockMiner (CGMiner + HTTP)
        "elphapex": [],        # Elphapex DG1, DG Home (Scrypt miners)
        "qaxe": [],            # QAxe quad-ASIC miner (~2 TH/s)
        "qaxeplus": [],        # QAxe+ enhanced cooling variant
        "esp32miner": [],       # ESP32 Miner ESP32-based solo miner
        "canaan": []           # Canaan AvalonMiner (A13, A14 series)
    },
    "refresh_interval": 30,  # seconds
    "theme": "cyberpunk",
    # Currency display preference (earnings calculator)
    "display_currency": "CAD",             # Display currency for earnings
    # Power cost configuration
    "power_cost": {
        "currency": "CAD",           # USD, CAD, EUR, GBP, etc.
        "currency_symbol": "$",
        "rate_per_kwh": 0.12,        # Cost per kWh
        "is_free_power": False       # Easter egg flag
    }
}

# Global state for caching miner data
miner_cache = {
    "last_update": 0,  # Init to 0 so first request triggers immediate fetch
    "miners": {},
    "totals": {
        "hashrate_ths": 0,
        "power_watts": 0,
        "accepted_shares": 0,
        "rejected_shares": 0,
        "blocks_found": 0
    }
}

# Supported fiat currencies (same as Sentinel — mirrors SUPPORTED_CURRENCIES)
DASHBOARD_CURRENCIES = {
    "USD": {"symbol": "$", "emoji": "\U0001f985", "name": "US Dollar", "code": "usd", "decimals": 2},
    "CAD": {"symbol": "$", "emoji": "\U0001f341", "name": "Canadian Dollar", "code": "cad", "decimals": 2},
    "EUR": {"symbol": "\u20ac", "emoji": "\U0001f310", "name": "Euro", "code": "eur", "decimals": 2},
    "GBP": {"symbol": "\u00a3", "emoji": "\U0001f310", "name": "British Pound", "code": "gbp", "decimals": 2},
    "JPY": {"symbol": "\u00a5", "emoji": "\U0001f310", "name": "Japanese Yen", "code": "jpy", "decimals": 0},
    "AUD": {"symbol": "$", "emoji": "\U0001f310", "name": "Australian Dollar", "code": "aud", "decimals": 2},
    "CHF": {"symbol": "Fr.", "emoji": "\U0001f310", "name": "Swiss Franc", "code": "chf", "decimals": 2},
    "CNY": {"symbol": "\u00a5", "emoji": "\U0001f310", "name": "Chinese Yuan", "code": "cny", "decimals": 2},
    "NZD": {"symbol": "$", "emoji": "\U0001f310", "name": "New Zealand Dollar", "code": "nzd", "decimals": 2},
    "SEK": {"symbol": "kr", "emoji": "\U0001f310", "name": "Swedish Krona", "code": "sek", "decimals": 2},
}
DASHBOARD_VS_CURRENCIES = ",".join(c["code"] for c in DASHBOARD_CURRENCIES.values())

# Cache for block reward info (multi-coin aware)
# NOTE: No default coin - will be populated from detected config
block_reward_cache = {
    "last_update": 0,
    "coin": None,           # Active coin symbol - detected from config
    "coin_name": None,      # Full coin name - detected from config
    "block_height": 0,
    "block_reward": 0,      # Current block reward in coin units
    "price_usd": 0,         # Coin price in USD (backward compat)
    "price_cad": 0,         # Coin price in CAD (backward compat)
    "block_time": 0         # Block time in seconds - depends on coin
}
# Initialize all currency price slots
for _cur in DASHBOARD_CURRENCIES.values():
    block_reward_cache[f"price_{_cur['code']}"] = 0

# Lifetime stats persistence
lifetime_stats = {
    "total_shares": 0,
    "total_pool_shares": 0,
    "total_blocks": 0,
    "best_share_difficulty": 0,
    "uptime_start": None,
    "total_runtime_seconds": 0
}

# ============================================
# POOL API & PROMETHEUS INTEGRATION
# ============================================

# Pool API configuration
POOL_API_URL = os.environ.get("POOL_API_URL", "http://127.0.0.1:4000")
PROMETHEUS_URL = os.environ.get("PROMETHEUS_URL", "http://127.0.0.1:9100")
_METRICS_AUTH_TOKEN_ENV = os.environ.get("SPIRAL_METRICS_TOKEN", "")  # Bearer token for /metrics endpoint
_METRICS_AUTH_TOKEN_CACHE = None  # Cached value from config file
_STRATUM_ADMIN_API_KEY_ENV = os.environ.get("SPIRAL_ADMIN_API_KEY", "")  # From environment
_STRATUM_ADMIN_API_KEY_CACHE = None  # Cached value from config file
_POOL_ID_ENV = os.environ.get("POOL_ID", "")  # User override if set


def get_stratum_admin_api_key():
    """Get the stratum admin API key from environment, dashboard config, or stratum config.

    Priority:
      1) SPIRAL_ADMIN_API_KEY env var
      2) pool_admin_api_key in dashboard config
      3) adminApiKey in stratum config.yaml
    """
    global _STRATUM_ADMIN_API_KEY_CACHE

    # Environment variable takes priority
    if _STRATUM_ADMIN_API_KEY_ENV:
        return _STRATUM_ADMIN_API_KEY_ENV

    # Use cached config value if available
    if _STRATUM_ADMIN_API_KEY_CACHE is not None:
        return _STRATUM_ADMIN_API_KEY_CACHE

    # Try to load from dashboard config file
    try:
        if CONFIG_FILE.exists():
            with open(CONFIG_FILE, 'r') as f:
                config = json.load(f)
                key = config.get("pool_admin_api_key", "")
                if key:
                    _STRATUM_ADMIN_API_KEY_CACHE = key
                    return _STRATUM_ADMIN_API_KEY_CACHE
    except Exception as e:
        logging.warning("Failed to load admin API key from %s: %s", CONFIG_FILE, e)

    # Fallback: Try to load from stratum config.yaml
    try:
        import yaml
        stratum_config_path = Path("/spiralpool/config/config.yaml")
        if stratum_config_path.exists():
            with open(stratum_config_path, 'r') as f:
                stratum_config = yaml.safe_load(f)
                if stratum_config:
                    # Check api.adminApiKey (new format)
                    api_section = stratum_config.get("api", {})
                    key = api_section.get("adminApiKey", "")
                    if key:
                        _STRATUM_ADMIN_API_KEY_CACHE = key
                        return _STRATUM_ADMIN_API_KEY_CACHE
                    # Check global.admin_api_key (alternate format)
                    global_section = stratum_config.get("global", {})
                    key = global_section.get("admin_api_key", "")
                    if key:
                        _STRATUM_ADMIN_API_KEY_CACHE = key
                        return _STRATUM_ADMIN_API_KEY_CACHE
    except Exception as e:
        print(f"[API-KEY] Failed to load admin API key from stratum config: {e}")

    _STRATUM_ADMIN_API_KEY_CACHE = ""
    return ""


_detected_pool_id = None  # Will be populated dynamically from API
_verified_spiral_stratum = None  # Track if we've verified this is Spiral Stratum
_verified_spiral_stratum_time = 0  # Timestamp of last verification attempt


def verify_spiral_stratum():
    """
    Verify that the pool API is Spiral Stratum (not another pool software).
    Returns True if verified, False if not Spiral Stratum or unavailable.
    This prevents the dashboard from showing data from other pool software.
    Positive results are cached permanently; negative results retry after 60s.
    """
    global _verified_spiral_stratum, _verified_spiral_stratum_time

    # Positive result cached permanently
    if _verified_spiral_stratum is True:
        return True

    # Negative result cached for 60 seconds (retry after temporary failures)
    if _verified_spiral_stratum is False and (time.time() - _verified_spiral_stratum_time) < 60:
        return False

    try:
        response = requests.get(f"{POOL_API_URL}/api/pools", timeout=5)
        if response.status_code == 200:
            data = response.json()
            # Check for Spiral Stratum identifier
            if data.get("software") == "spiral-stratum":
                _verified_spiral_stratum = True
                _verified_spiral_stratum_time = time.time()
                return True
    except Exception:
        pass

    _verified_spiral_stratum = False
    _verified_spiral_stratum_time = time.time()
    return False


def reset_spiral_stratum_verification():
    """Reset the verification cache to force re-verification."""
    global _verified_spiral_stratum
    _verified_spiral_stratum = None


# ============================================
# LOCAL POOL DETECTION FOR MINER FILTERING
# ============================================
# Cache for local pool addresses (hostname:port combinations that identify THIS pool)
_local_pool_addresses = None
_local_pool_addresses_last_update = 0

# Additional local pool addresses from environment (comma-separated)
# Example: LOCAL_POOL_ADDRESSES="192.168.1.100,mypool.local,10.0.0.50:3333"
_EXTRA_LOCAL_ADDRESSES = os.environ.get("LOCAL_POOL_ADDRESSES", "").strip()

# Force counting ALL shares regardless of pool URL detection
# Set to "true" for single-pool setups where all miners connect to your pool
# Example: COUNT_ALL_SHARES=true
_COUNT_ALL_SHARES = os.environ.get("COUNT_ALL_SHARES", "").lower() in ("true", "1", "yes")


def get_local_pool_addresses():
    """
    Get all valid stratum addresses for the local Spiral Stratum pool.

    Returns a set of lowercase address patterns that can be matched against
    miner-reported pool_url values. Includes:
    - localhost variants (127.0.0.1, localhost, ::1)
    - Server's actual hostname/IP
    - VIP address (if HA is enabled)
    - All configured stratum ports

    This is used to filter miner stats to only count shares from miners
    connected to THIS pool, not other pools they might be mining to.
    """
    global _local_pool_addresses, _local_pool_addresses_last_update

    # Cache for 60 seconds
    now = time.time()
    if _local_pool_addresses and (now - _local_pool_addresses_last_update) < 60:
        return _local_pool_addresses

    addresses = set()

    # Get stratum ports from config
    stratum_ports = set()
    for symbol, node in MULTI_COIN_NODES.items():
        if node.get('enabled', False):
            ports = node.get('stratum_ports', {})
            if ports.get('v1'):
                stratum_ports.add(ports['v1'])
            if ports.get('v2'):
                stratum_ports.add(ports['v2'])
            if ports.get('tls'):
                stratum_ports.add(ports['tls'])

    # Default ports if nothing found - common stratum ports for all coins
    if not stratum_ports:
        stratum_ports = {3333, 3334, 3335}  # Common stratum ports (not coin-specific)

    # Local addresses
    local_hosts = ['127.0.0.1', 'localhost', '::1', '0.0.0.0']

    # Try to get server hostname/IP
    try:
        import socket
        hostname = socket.gethostname()
        local_hosts.append(hostname)
        local_hosts.append(hostname.lower())

        # Get all IPs for this host via hostname resolution
        try:
            for info in socket.getaddrinfo(hostname, None):
                ip = info[4][0]
                if ip not in local_hosts:
                    local_hosts.append(ip)
        except socket.gaierror:
            pass

        # Also try to get LAN IP by connecting to external address
        # This reliably gets the IP that other devices on the LAN see
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.settimeout(0.1)
            s.connect(('8.8.8.8', 80))  # Doesn't actually send data
            lan_ip = s.getsockname()[0]
            s.close()
            if lan_ip and lan_ip not in local_hosts:
                local_hosts.append(lan_ip)
        except Exception:
            pass

        # Try to get all network interface IPs
        try:
            import subprocess
            # Windows: ipconfig, Linux: hostname -I or ip addr
            if os.name == 'nt':
                result = subprocess.run(['ipconfig'], capture_output=True, text=True, timeout=5)
                for line in result.stdout.split('\n'):
                    if 'IPv4' in line and ':' in line:
                        ip = line.split(':')[-1].strip()
                        if ip and ip not in local_hosts:
                            local_hosts.append(ip)
            else:
                result = subprocess.run(['hostname', '-I'], capture_output=True, text=True, timeout=5)
                for ip in result.stdout.strip().split():
                    if ip and ip not in local_hosts:
                        local_hosts.append(ip)
        except Exception:
            pass
    except Exception:
        pass

    # Try to get VIP from HA status
    try:
        ha_status = fetch_ha_status()
        if ha_status and ha_status.get('enabled') and ha_status.get('vip'):
            vip = ha_status['vip']
            local_hosts.append(vip)
    except Exception:
        pass

    # Try to get bind address from pool config
    try:
        config_path = "/spiralpool/config/config.yaml"
        if os.path.exists(config_path):
            import yaml
            with open(config_path, 'r') as f:
                config = yaml.safe_load(f)

            # Check stratum bind address
            if config and 'stratum' in config:
                bind_addr = config['stratum'].get('listenAddress', '')
                if bind_addr and ':' in bind_addr:
                    host, port = bind_addr.rsplit(':', 1)
                    if host and host not in local_hosts:
                        local_hosts.append(host)
                    try:
                        stratum_ports.add(int(port))
                    except ValueError:
                        pass

            # Check coin-specific stratum configs
            for coin in config.get('coins', []):
                stratum_cfg = coin.get('stratum', {})
                for port_key in ['port', 'portV2', 'portTLS']:
                    if stratum_cfg.get(port_key):
                        try:
                            stratum_ports.add(int(stratum_cfg[port_key]))
                        except ValueError:
                            pass
    except Exception:
        pass

    # Build all combinations of host:port
    for host in local_hosts:
        # Without port (some APIs report just hostname)
        addresses.add(host.lower())

        # With each stratum port
        for port in stratum_ports:
            addresses.add(f"{host.lower()}:{port}")
            # Also handle stratum:// prefix formats
            addresses.add(f"stratum+tcp://{host.lower()}:{port}")
            addresses.add(f"stratum+ssl://{host.lower()}:{port}")

    # Add any extra addresses from environment variable
    # Format: LOCAL_POOL_ADDRESSES="192.168.1.100,mypool.local:3333,10.0.0.50"
    if _EXTRA_LOCAL_ADDRESSES:
        for addr in _EXTRA_LOCAL_ADDRESSES.split(','):
            addr = addr.strip().lower()
            if addr:
                addresses.add(addr)
                # If it's just a hostname, also add with ports
                if ':' not in addr:
                    for port in stratum_ports:
                        addresses.add(f"{addr}:{port}")

    _local_pool_addresses = addresses
    _local_pool_addresses_last_update = now
    return addresses


def is_miner_connected_to_local_pool(pool_url):
    """
    Check if a miner's reported pool_url is connected to the local Spiral Stratum.

    Args:
        pool_url: The pool URL reported by the miner's API (e.g., "192.168.1.100:3333")

    Returns:
        True if connected to local pool, False if connected elsewhere, None if unknown
    """
    if not pool_url:
        return None  # Unknown - miner didn't report pool URL

    pool_url_lower = pool_url.lower().strip()

    # Remove protocol prefix for comparison
    for prefix in ['stratum+tcp://', 'stratum+ssl://', 'stratum://', 'tcp://', 'ssl://']:
        if pool_url_lower.startswith(prefix):
            pool_url_lower = pool_url_lower[len(prefix):]
            break

    # Remove trailing slash
    pool_url_lower = pool_url_lower.rstrip('/')

    local_addresses = get_local_pool_addresses()

    # Direct match
    if pool_url_lower in local_addresses:
        return True

    # Check if host part matches (for cases where port differs)
    if ':' in pool_url_lower:
        host_only = pool_url_lower.rsplit(':', 1)[0]
        if host_only in local_addresses:
            return True

    return False


def get_pool_id():
    """
    Get the pool ID, dynamically detecting from the API if not set via environment.
    This ensures the dashboard works regardless of what pool_id is configured in stratum.
    """
    global _detected_pool_id

    # If user set POOL_ID via environment, use that
    if _POOL_ID_ENV:
        return _POOL_ID_ENV

    # If we've already detected it, return cached value
    if _detected_pool_id:
        return _detected_pool_id

    # Try to detect from API
    try:
        response = requests.get(f"{POOL_API_URL}/api/pools", timeout=5)
        if response.status_code == 200:
            data = response.json()
            if data.get("pools") and len(data["pools"]) > 0:
                # Get the first pool's ID from API response (coin-agnostic)
                _detected_pool_id = data["pools"][0].get("id")
                if _detected_pool_id:
                    return _detected_pool_id
    except Exception:
        pass

    # Last resort fallback: return a placeholder that will be used in API calls
    # The API will return the first available pool regardless of ID
    # This handles edge cases where API detection fails during startup
    return "unknown_pool_id"

# Blockchain Node RPC configuration (auto-loaded from pool config)
# These are generic names - works for DGB, BTC, or BCH based on what's configured
BLOCKCHAIN_RPC_HOST = "127.0.0.1"
BLOCKCHAIN_RPC_PORT = None  # Must be loaded from config - no default to avoid wrong coin
BLOCKCHAIN_RPC_USER = ""
BLOCKCHAIN_RPC_PASSWORD = ""
ACTIVE_COIN_SYMBOL = None  # Must be detected from config - no default to avoid wrong coin
POOL_CONFIG_PATH = "/spiralpool/config/config.yaml"
_multi_coin_config_loaded = False  # One-shot flag: prevent lazy-init from firing on every call

# Health cache
health_cache = {
    "last_update": 0,
    "pool": {},
    "node": {}
}

# Pool stats cache
pool_stats_cache = {
    "last_update": 0,
    "connected_miners": 0,
    "pool_hashrate": 0,
    "shares_per_second": 0,
    "network_difficulty": 0,
    "block_height": 0,
    "blocks_found": -1,
    "blocks_pending": 0,
    "last_block_time": None,
    "last_block_finder": None,  # Miner that found the last block
    "last_block_height": None,
    "status": "unknown"
}

# ============================================
# HIGH AVAILABILITY (HA) STATUS INTEGRATION
# ============================================

# HA status port (matches VIP manager in Go)
try:
    HA_STATUS_PORT = int(os.environ.get("HA_STATUS_PORT", "5354"))
except (ValueError, TypeError):
    HA_STATUS_PORT = 5354

# HA cache with exponential backoff parameters
HA_CACHE_TTL_MIN = 5        # Minimum cache TTL in seconds (when healthy)
HA_CACHE_TTL_MAX = 300      # Maximum cache TTL in seconds (after repeated failures)
HA_CACHE_BACKOFF_FACTOR = 2 # Exponential backoff multiplier

# HA status cache with exponential backoff support
ha_status_cache = {
    "last_update": 0,
    "enabled": False,
    "state": "unknown",
    "vip": "",
    "vip_interface": "",
    "master_id": "",
    "master_host": "",
    "local_role": "UNKNOWN",
    "local_id": "",
    "node_count": 0,
    "healthy_nodes": 0,
    "failover_count": 0,
    "stratum_address": "",
    # Exponential backoff tracking
    "_consecutive_failures": 0,
    "_current_ttl": HA_CACHE_TTL_MIN,
    "_last_error": None
}

def fetch_ha_status():
    """
    Fetch HA/VIP cluster status from the pool's HA status endpoint.
    Only updates cache if HA is detected/enabled.

    Uses exponential backoff for cache TTL on failures:
    - Success: TTL resets to minimum (5 seconds)
    - Failure: TTL doubles up to maximum (5 minutes)
    This prevents excessive requests when HA is unavailable while
    remaining responsive when it becomes available again.
    """
    global ha_status_cache

    now = time.time()

    # Use exponential backoff TTL
    current_ttl = ha_status_cache.get("_current_ttl", HA_CACHE_TTL_MIN)
    if now - ha_status_cache["last_update"] < current_ttl:
        return ha_status_cache

    try:
        # Try to connect to HA status endpoint
        response = requests.get(
            f"http://127.0.0.1:{HA_STATUS_PORT}/status",
            timeout=TIMEOUT_FAST,
            headers={"User-Agent": "SpiralDashboard/1.0"}
        )

        if response.status_code == 200:
            data = response.json()

            # Only populate if HA is actually enabled
            if data.get("enabled", False):
                nodes = data.get("nodes", [])
                healthy_count = sum(1 for n in nodes if n.get("isHealthy", False))

                # Get stratum port from first node
                stratum_port = 3333  # Common default for all coins
                if nodes:
                    stratum_port = nodes[0].get("stratumPort", 3333)

                # Success: Reset backoff to minimum TTL
                ha_status_cache = {
                    "last_update": now,
                    "enabled": True,
                    "state": data.get("state", "unknown"),
                    "vip": data.get("vip", ""),
                    "vip_interface": data.get("vipInterface", ""),
                    "master_id": data.get("masterId", ""),
                    "master_host": data.get("masterHost", ""),
                    "local_role": data.get("localRole", "UNKNOWN"),
                    "local_id": data.get("localId", ""),
                    "node_count": len(nodes),
                    "healthy_nodes": healthy_count,
                    "failover_count": data.get("failoverCount", 0),
                    "stratum_address": f"{data.get('vip', '')}:{stratum_port}" if data.get("vip") else "",
                    "_consecutive_failures": 0,
                    "_current_ttl": HA_CACHE_TTL_MIN,
                    "_last_error": None
                }
            else:
                # HA endpoint exists but HA is disabled - reset backoff
                ha_status_cache["last_update"] = now
                ha_status_cache["enabled"] = False
                ha_status_cache["_consecutive_failures"] = 0
                ha_status_cache["_current_ttl"] = HA_CACHE_TTL_MIN
                ha_status_cache["_last_error"] = None

    except (requests.exceptions.ConnectionError, requests.exceptions.Timeout) as e:
        # HA endpoint not available - apply exponential backoff
        failures = ha_status_cache.get("_consecutive_failures", 0) + 1
        new_ttl = min(
            HA_CACHE_TTL_MIN * (HA_CACHE_BACKOFF_FACTOR ** failures),
            HA_CACHE_TTL_MAX
        )

        ha_status_cache["last_update"] = now
        ha_status_cache["enabled"] = False
        ha_status_cache["_consecutive_failures"] = failures
        ha_status_cache["_current_ttl"] = new_ttl
        ha_status_cache["_last_error"] = "connection_timeout"

        if failures <= 3:  # Only log first few failures
            print(f"[HA] Connection failed (attempt {failures}), next check in {new_ttl:.0f}s")

    except Exception as e:
        # Other error - apply backoff
        failures = ha_status_cache.get("_consecutive_failures", 0) + 1
        new_ttl = min(
            HA_CACHE_TTL_MIN * (HA_CACHE_BACKOFF_FACTOR ** failures),
            HA_CACHE_TTL_MAX
        )

        ha_status_cache["last_update"] = now
        ha_status_cache["enabled"] = False
        ha_status_cache["_consecutive_failures"] = failures
        ha_status_cache["_current_ttl"] = new_ttl
        ha_status_cache["_last_error"] = str(e)[:50]

    return ha_status_cache


def reset_ha_cache():
    """
    Force reset the HA cache, clearing backoff state.
    Useful when HA config changes or on admin request.
    """
    global ha_status_cache
    ha_status_cache["last_update"] = 0
    ha_status_cache["_consecutive_failures"] = 0
    ha_status_cache["_current_ttl"] = HA_CACHE_TTL_MIN
    ha_status_cache["_last_error"] = None

# Prometheus metrics cache
prometheus_cache = {
    "last_update": 0,
    "metrics": {}
}

# Historical data storage (keeps last 7 days of data points at 1-minute intervals)
HISTORY_MAX_POINTS = 10080  # 1 point per minute for 7 days
historical_data = {
    "pool_hashrate": deque(maxlen=HISTORY_MAX_POINTS),
    "miner_hashrate": deque(maxlen=HISTORY_MAX_POINTS),
    "connected_miners": deque(maxlen=HISTORY_MAX_POINTS),
    "shares_per_second": deque(maxlen=HISTORY_MAX_POINTS),
    "power_watts": deque(maxlen=HISTORY_MAX_POINTS),
    "temperatures": deque(maxlen=HISTORY_MAX_POINTS),
    "network_difficulty": deque(maxlen=HISTORY_MAX_POINTS),
    "network_hashrate": deque(maxlen=HISTORY_MAX_POINTS),
    "per_miner_hashrate": {}  # {name: deque(maxlen=HISTORY_MAX_POINTS)} - per-miner hashrate tracking
}

# Alert configuration for UI display (notifications handled by Spiral Sentinel)
alert_config = {
    "enabled": True,
    "hashrate_drop_percent": 50,  # Alert if hashrate drops 50%
    "hashrate_min_ths": 0,        #  Minimum hashrate threshold (TH/s) - 0 = disabled
    "miner_hashrate_drop_percent": 30,  #  Per-miner hashrate drop alert
    "miner_offline_minutes": 5,
    "temp_warning": 70,
    "temp_critical": 80,
    "check_interval": 60  # seconds
    # NOTE: Discord/Telegram notifications are handled by Spiral Sentinel
}

# Alert state tracking ( Enhanced with per-miner tracking)
alert_state = {
    "last_hashrate": 0,
    "alerts_triggered": [],
    "miner_last_seen": {},
    "miner_last_hashrate": {},    #  Track per-miner hashrate
    "alert_history": []           #  Keep history of alerts
}

#  Block explorer cache for found blocks
block_explorer_cache = {
    "found_blocks": [],           # List of blocks we've found
    "last_update": 0,
    "pool_wallet_txs": []         # Recent transactions for pool wallet
}

# V1.0: Share submission heatmap (24h x 7 days grid)
share_heatmap = {
    "data": [[0 for _ in range(24)] for _ in range(7)],  # [day][hour] = share_count
    "last_reset": time.time()
}

# V1.0: Firmware version tracking
firmware_tracker = {
    "miners": {},          # {ip: {version, device_type, last_seen, update_available}}
    "known_versions": {    # Latest known firmware versions
        "bitaxe": "2.4.0",
        "antminer_s19": "Antminer-S19-202312",
        "antminer_s21": "Antminer-S21-202401",
        "whatsminer_m50": "20231215",
        "whatsminer_m60": "20240115"
    },
    "last_update": 0
}

# V1.0: Miner downtime tracking
downtime_tracker = {
    "miners": {},          # {ip: {total_downtime_sec, downtime_events: [], last_online, current_status}}
    "events": [],          # [{miner_ip, start_time, end_time, duration_sec}]
    "last_update": 0
}

# Activity Feed — unified event timeline for the dashboard
activity_feed = {
    "events": deque(maxlen=500),
    "last_save": 0
}

# V1.0: Performance degradation tracking
performance_tracker = {
    "miners": {},          # {ip: {hashrate_baseline, hashrate_samples: [], degradation_percent}}
    "degradation_threshold": 10,  # Alert if hashrate drops 10% below baseline
    "sample_window_hours": 24,
    "last_update": 0
}

# V1.0: Share rejection analysis
rejection_analysis = {
    "total_accepted": 0,
    "total_rejected": 0,
    "rejection_reasons": {},  # {reason: count}
    "by_miner": {},          # {ip: {accepted, rejected, rejection_rate}}
    "hourly_rejection_rate": [],  # [{timestamp, rate}]
    "last_update": 0
}

# V1.0: Uptime tracking per miner
uptime_tracker = {
    "miners": {},          # {ip: {uptime_seconds, uptime_percent, last_reboot, online_since}}
    "farm_uptime_start": time.time(),
    "farm_total_uptime_seconds": 0,
    "last_update": 0
}

# V1.0: Power cost and profitability tracking
power_cost_tracker = {
    "daily_kwh": 0,
    "daily_cost": 0,
    "monthly_kwh": 0,
    "monthly_cost": 0,
    "daily_earnings_dgb": 0,
    "daily_profit": 0,        # earnings - cost
    "history": [],            # [{date, kwh, cost, earnings, profit}]
    "last_update": 0
}

# NOTE: Hashrate Watchdog with Auto-Restart is handled by Spiral Sentinel
# See: src/sentinel/SpiralSentinel.py - check_zombie_miner() and auto-restart logic

# V1.0: WebSocket connected clients tracking
websocket_clients = {
    "count": 0,
    "last_broadcast": 0
}
_ws_lock = threading.Lock()

# V1.0: Share Audit Log - Every share timestamped and indexed for proof of work
SHARE_AUDIT_FILE = CONFIG_DIR / "share_audit_log.json"
share_audit_log = {
    "shares": [],              # [{index, timestamp, miner_ip, worker, difficulty, hash_prefix, accepted}]
    "total_shares": 0,
    "session_start": time.time(),
    "last_index": 0,
    "max_entries": 100000      # Keep last 100k shares (rotate older ones)
}

# V1.0: Session Statistics - Stats since last dashboard restart
session_stats = {
    "start_time": time.time(),
    "shares_submitted": 0,
    "shares_accepted": 0,
    "shares_rejected": 0,
    "blocks_found": 0,
    "best_share_difficulty": 0,
    "total_hashrate_samples": [],
    "peak_hashrate_ths": 0,
    "miners_connected_max": 0,
    "restarts_triggered": 0
}

# V1.0: Estimated Time to Block (ETB) calculation
etb_calculator = {
    "current_hashrate_ths": 0,
    "network_difficulty": 0,
    "network_hashrate_ths": 0,
    "estimated_seconds": 0,
    "probability_24h": 0,
    "probability_7d": 0,
    "probability_30d": 0,
    "last_update": 0
}

# V1.0: Luck Tracker - Track actual vs expected blocks
luck_tracker = {
    "blocks_found": 0,              # Actual blocks found
    "blocks_expected": 0.0,         # Expected blocks based on hashrate/difficulty
    "luck_percent": 100.0,          # Current luck (100% = exactly as expected)
    "luck_history": [],             # [{timestamp, blocks_found, blocks_expected, luck}]
    "shares_since_last_block": 0,
    "expected_shares_per_block": 0,
    "last_block_time": 0,
    "average_block_time": 0,        # Running average
    "last_update": 0
}

# V1.0: Difficulty Adjustment Predictor - Estimate next difficulty change
difficulty_predictor = {
    "current_difficulty": 0,
    "previous_difficulty": 0,
    "difficulty_history": [],       # [{timestamp, difficulty, change_percent}]
    "predicted_next_difficulty": 0,
    "predicted_change_percent": 0,
    "blocks_until_adjustment": 0,   # DigiByte adjusts every block, but we track trends
    "trend": "stable",              # "increasing", "decreasing", "stable"
    "last_update": 0
}


def fetch_pool_stats():
    """Fetch statistics from the Pool API (Spiral Stratum only)"""
    global pool_stats_cache

    # Rate limit to every 10 seconds
    if time.time() - pool_stats_cache["last_update"] < 10:
        return pool_stats_cache

    try:
        # Get pool info (network I/O happens OUTSIDE lock)
        response = requests.get(
            f"{POOL_API_URL}/api/pools",
            timeout=5
        )
        response.raise_for_status()
        data = response.json()

        # SECURITY: Verify this is Spiral Stratum, not another pool software
        # This prevents showing stats from other pools that may be running
        if data.get("software") != "spiral-stratum":
            with _pool_stats_lock:
                pool_stats_cache["status"] = "wrong_pool"
                pool_stats_cache["connected_miners"] = 0
                pool_stats_cache["pool_hashrate"] = 0
                pool_stats_cache["shares_per_second"] = 0
                pool_stats_cache["last_update"] = time.time()
            return pool_stats_cache

        # Fetch blocks info (network I/O outside lock)
        blocks_data = None
        try:
            blocks_response = requests.get(
                f"{POOL_API_URL}/api/pools/{get_pool_id()}/blocks",
                timeout=5
            )
            if blocks_response.status_code == 200:
                blocks_data = blocks_response.json()
        except (requests.exceptions.RequestException, ValueError, KeyError):
            pass

        # Update all cache fields atomically under lock
        with _pool_stats_lock:
            got_pool_data = False
            if data.get("pools") and len(data["pools"]) > 0:
                pool = data["pools"][0]
                stats = pool.get("poolStats", {})

                pool_stats_cache["connected_miners"] = stats.get("connectedMiners", 0)
                pool_stats_cache["pool_hashrate"] = stats.get("poolHashrate", 0)
                pool_stats_cache["shares_per_second"] = stats.get("sharesPerSecond", 0)
                pool_stats_cache["network_difficulty"] = stats.get("networkDifficulty", 0)
                pool_stats_cache["block_height"] = stats.get("blockHeight", 0)
                pool_stats_cache["status"] = "online"
                got_pool_data = True

            new_blocks_to_announce = []
            if blocks_data is not None:
                blocks = blocks_data
                old_count = pool_stats_cache["blocks_found"]
                new_count = len(blocks)
                pool_stats_cache["blocks_found"] = new_count
                pool_stats_cache["blocks_pending"] = sum(1 for b in blocks if b.get("status") == "pending")
                if blocks:
                    latest_block = blocks[0]
                    pool_stats_cache["last_block_time"] = latest_block.get("created")
                    pool_stats_cache["last_block_height"] = latest_block.get("blockHeight")
                    # Get the miner who found the block (worker name or address)
                    pool_stats_cache["last_block_finder"] = latest_block.get("source") or latest_block.get("worker") or latest_block.get("miner") or latest_block.get("minerAddress")
                # Detect newly found blocks (count increased since last poll).
                # old_count == -1 on the very first poll — just establishes the baseline,
                # no celebration (avoids false-triggering existing blocks on startup).
                # All subsequent increases (old_count >= 0) trigger the celebration.
                if new_count > old_count and old_count >= 0:
                    # Announce the newest blocks (difference = new blocks found since last check)
                    num_new = new_count - old_count
                    new_blocks_to_announce = blocks[:num_new]
                got_pool_data = True

            # Only update last_update if we got actual data — prevents caching
            # false zeros and blocking retries for 10 seconds
            if got_pool_data:
                pool_stats_cache["last_update"] = time.time()

        # Broadcast new blocks OUTSIDE the lock (avoids holding lock during I/O)
        for blk in new_blocks_to_announce:
            try:
                broadcast_block_found({
                    "coin": blk.get("coin", ""),
                    "height": blk.get("blockHeight", "?"),
                    "miner": blk.get("miner", ""),
                    "worker": blk.get("worker", ""),
                    "source": blk.get("source", ""),
                    "reward": blk.get("reward", 0),
                    "hash": blk.get("hash", ""),
                    "status": blk.get("status", ""),
                    "created": blk.get("created", ""),
                })
            except Exception as e:
                print(f"[ACTIVITY] Error broadcasting block: {e}")
        if new_blocks_to_announce:
            save_activity_feed()

    except requests.exceptions.ConnectionError:
        with _pool_stats_lock:
            pool_stats_cache["status"] = "offline"
    except Exception as e:
        print(f"Error fetching pool stats: {e}")
        with _pool_stats_lock:
            pool_stats_cache["status"] = "error"

    return pool_stats_cache


# Cache for worker name mapping (IP -> worker name from stratum auth)
_worker_name_cache = {
    "mapping": {},  # IP -> worker name (excludes "default" workers)
    "connected_ips": set(),  # ALL connected IPs (including "default" workers)
    "esp32_connected": False,  # True if any ESP32/NerdMiner userAgent is connected
    "esp32_count": 0,  # Number of ESP32/NerdMiner connections
    "esp32_connections": [],  # List of ESP32 connection dicts from pool API
    "last_update": 0
}
_worker_name_lock = threading.Lock()


def fetch_worker_name_mapping():
    """Fetch worker names from pool connections API.

    Returns a dict mapping IP addresses to stratum worker names.
    This allows the dashboard to display the worker name (e.g., "Heat2Sats")
    instead of just the IP address.

    The worker name comes from the stratum mining.authorize call,
    where miners authenticate as "ADDRESS.workername".
    """
    global _worker_name_cache

    with _worker_name_lock:
        # Rate limit to every 30 seconds (inside lock to prevent thundering herd)
        if time.time() - _worker_name_cache["last_update"] < 30:
            return _worker_name_cache["mapping"]

        try:
            pool_id = get_pool_id()
            if not pool_id:
                return _worker_name_cache["mapping"]

            # Build headers - connections endpoint requires admin API key
            headers = {}
            admin_key = get_stratum_admin_api_key()
            if admin_key:
                headers["X-Api-Key"] = admin_key

            response = requests.get(
                f"{POOL_API_URL}/api/pools/{pool_id}/connections?limit=1000",
                headers=headers,
                timeout=5
            )
            response.raise_for_status()
            data = response.json()

            connections = data.get("connections", [])
            mapping = {}
            connected_ips = set()
            esp32_count = 0
            esp32_connections = []

            for conn in connections:
                remote_addr = conn.get("remoteAddr", "")
                worker_name = conn.get("workerName", "")
                user_agent = conn.get("userAgent", "").lower()

                # Detect ESP32/NerdMiner connections by userAgent
                if "nerdminer" in user_agent or "esp32" in user_agent:
                    esp32_count += 1
                    esp32_connections.append({
                        "workerName": conn.get("workerName", "default"),
                        "minerAddress": conn.get("minerAddress", ""),
                        "userAgent": conn.get("userAgent", ""),
                        "difficulty": conn.get("difficulty", 0),
                        "shareCount": conn.get("shareCount", 0),
                        "connectedAt": conn.get("connectedAt", ""),
                        "lastActivity": conn.get("lastActivity", ""),
                    })

                if remote_addr:
                    # Extract just the IP (remove port if present)
                    ip = remote_addr.split(":")[0] if ":" in remote_addr else remote_addr
                    if ip:
                        # Track ALL connected IPs for online detection
                        connected_ips.add(ip)
                        # Only store named workers in the mapping (not "default")
                        if worker_name and worker_name != "default":
                            mapping[ip] = worker_name

            _worker_name_cache["mapping"] = mapping
            _worker_name_cache["connected_ips"] = connected_ips
            _worker_name_cache["esp32_connected"] = esp32_count > 0
            _worker_name_cache["esp32_count"] = esp32_count
            _worker_name_cache["esp32_connections"] = esp32_connections
            _worker_name_cache["last_update"] = time.time()

        except Exception as e:
            print(f"Error fetching worker name mapping: {e}")

        return _worker_name_cache["mapping"]


def get_worker_name_for_ip(ip: str, default_name: str = None) -> str:
    """Get the stratum worker name for a miner IP.

    Falls back to default_name if no mapping found.
    This lets the dashboard show "Heat2Sats" instead of "192.168.1.14".
    """
    mapping = fetch_worker_name_mapping()
    return mapping.get(ip, default_name or ip)


def is_device_connected_via_stratum(ip: str) -> bool:
    """Check if a device IP appears in the stratum connections list.

    This is used to determine online status for devices without HTTP API
    (like ESP32 Miner) based on their stratum connection.
    Checks ALL connected IPs, including workers named "default".
    """
    fetch_worker_name_mapping()  # Ensure cache is populated
    return ip in _worker_name_cache.get("connected_ips", set())


def is_esp32_connected_via_stratum() -> bool:
    """Check if any ESP32/NerdMiner device is connected via stratum.

    ESP32 miners often connect as "default" worker and the pool may see
    all LAN devices from the same gateway IP, making IP-based detection
    unreliable. Instead, detect by userAgent string.
    """
    fetch_worker_name_mapping()  # Ensure cache is populated
    return _worker_name_cache.get("esp32_connected", False)


def fetch_pool_worker_stats(worker_name: str, miner_address: str = None) -> dict:
    """Fetch worker stats from the pool API.

    Returns pool-side statistics like hashrate, shares, etc.
    Used for devices without HTTP API to show accurate pool data.

    Args:
        worker_name: The stratum worker name (e.g., "cardy")
        miner_address: Optional wallet address for the worker

    Returns:
        Dict with worker stats or None if not available
    """
    try:
        pool_id = get_pool_id()
        if not pool_id:
            return None

        # If no miner address provided, try to get it from pool API
        if not miner_address:
            try:
                resp = requests.get(f"{POOL_API_URL}/api/pools", timeout=5)
                if resp.status_code == 200:
                    pools = resp.json().get("pools", [])
                    for pool in pools:
                        addr = pool.get("address", "")
                        if addr:
                            miner_address = addr
                            break
            except Exception:
                pass

        if not miner_address:
            return None

        from urllib.parse import quote
        safe_name = quote(worker_name, safe='')
        safe_address = quote(miner_address, safe='')

        url = f"{POOL_API_URL}/api/pools/{pool_id}/miners/{safe_address}/workers/{safe_name}"
        resp = requests.get(url, timeout=10)

        if resp.status_code == 200:
            return resp.json()
    except Exception as e:
        print(f"Error fetching pool worker stats for {worker_name}: {e}")

    return None


def extract_worker_from_pool_user(pool_user: str) -> str:
    """Extract the worker name from a stratum auth string.

    Stratum auth format: ADDRESS.workername (e.g., "dgb1qxyz...Heat2Sats")
    Returns just the worker name part, or None if not found.

    Handles various address formats:
    - Bitcoin/DigiByte: bc1..., dgb1..., D..., 1..., 3...
    - Any coin with ADDRESS.worker format
    """
    if not pool_user or "." not in pool_user:
        return None

    # Split on the last dot to handle addresses with multiple dots
    # e.g., "dgb1qxyz.Heat2Sats" -> "Heat2Sats"
    parts = pool_user.rsplit(".", 1)
    if len(parts) == 2:
        address_part, worker_part = parts
        # Validate that we actually have a worker name (not just noise)
        # Worker names should be alphanumeric with optional - or _
        if worker_part and len(worker_part) >= 1 and len(worker_part) <= 64:
            # Make sure the first part looks like an address (long alphanumeric string)
            if len(address_part) >= 20:
                return worker_part

    return None


def parse_prometheus_metrics(text):
    """Parse Prometheus metrics text format into a dict.

    For labeled metrics like stratum_shares_rejected_total{reason="stale"}, stores
    both the labeled key AND sums all variants into the bare metric name, so that
    metrics.get("stratum_shares_rejected_total") returns the total across all labels.
    """
    metrics = {}
    # Track sums for labeled metrics so bare name lookups work
    label_sums = {}
    for line in text.strip().split('\n'):
        line = line.strip()
        if not line or line.startswith('#'):
            continue

        # Parse metric line: metric_name{labels} value
        match = re.match(r'^([a-zA-Z_:][a-zA-Z0-9_:]*)\s*(?:\{([^}]*)\})?\s+(.+)$', line)
        if match:
            name = match.group(1)
            labels = match.group(2) or ""
            value = match.group(3)

            try:
                value = float(value)
            except ValueError:
                continue

            if labels:
                key = f"{name}{{{labels}}}"
                # Sum into base name for convenient lookups
                label_sums[name] = label_sums.get(name, 0) + value
            else:
                key = name

            metrics[key] = value

    # Merge label sums into metrics (only if bare name not already set)
    for name, total in label_sums.items():
        if name not in metrics:
            metrics[name] = total

    return metrics


def _get_metrics_auth_token():
    """Get the metrics auth token for Prometheus /metrics endpoint.

    Priority:
      1) SPIRAL_METRICS_TOKEN env var
      2) metrics_auth_token in dashboard config JSON
      3) metrics.authToken in stratum config.yaml
    """
    global _METRICS_AUTH_TOKEN_CACHE

    # Env var takes priority
    if _METRICS_AUTH_TOKEN_ENV:
        return _METRICS_AUTH_TOKEN_ENV

    # Use cached value if available
    if _METRICS_AUTH_TOKEN_CACHE is not None:
        return _METRICS_AUTH_TOKEN_CACHE

    # Try dashboard config JSON
    try:
        if CONFIG_FILE.exists():
            with open(CONFIG_FILE, 'r') as f:
                config = json.load(f)
                token = config.get("metrics_auth_token", "")
                if token:
                    _METRICS_AUTH_TOKEN_CACHE = token
                    return _METRICS_AUTH_TOKEN_CACHE
    except Exception:
        pass

    # Fallback: stratum config.yaml
    try:
        import yaml
        if os.path.exists(POOL_CONFIG_PATH):
            with open(POOL_CONFIG_PATH, 'r') as f:
                stratum_config = yaml.safe_load(f)
            token = stratum_config.get('metrics', {}).get('authToken', '')
            if token:
                _METRICS_AUTH_TOKEN_CACHE = token
                return _METRICS_AUTH_TOKEN_CACHE
    except Exception:
        pass

    _METRICS_AUTH_TOKEN_CACHE = ""  # Cache empty to avoid re-reading
    return ""


def fetch_prometheus_metrics():
    """Fetch and parse Prometheus metrics"""
    global prometheus_cache

    # Rate limit to every 15 seconds
    if time.time() - prometheus_cache["last_update"] < 15:
        return prometheus_cache["metrics"]

    try:
        headers = {}
        token = _get_metrics_auth_token()
        if token:
            headers["Authorization"] = f"Bearer {token}"

        response = requests.get(
            f"{PROMETHEUS_URL}/metrics",
            headers=headers,
            timeout=5
        )
        response.raise_for_status()

        prometheus_cache["metrics"] = parse_prometheus_metrics(response.text)
        prometheus_cache["last_update"] = time.time()

    except Exception as e:
        print(f"Error fetching Prometheus metrics: {e}")

    return prometheus_cache["metrics"]


def load_stratum_ports_from_config():
    """Load stratum ports from pool config.yaml and update MULTI_COIN_NODES.

    This ensures the dashboard displays the actual configured ports rather than
    hardcoded defaults. Supports both single-coin and multi-coin configurations.
    """
    global MULTI_COIN_NODES

    try:
        import yaml
        if not os.path.exists(POOL_CONFIG_PATH):
            return False

        with open(POOL_CONFIG_PATH, 'r') as f:
            config = yaml.safe_load(f)

        # Check for multi-coin configuration (V2 config)
        coins = config.get('coins', [])
        if coins:
            for coin_config in coins:
                symbol = coin_config.get('symbol', '').upper()
                if symbol in MULTI_COIN_NODES:
                    # Update stratum ports from config
                    stratum = coin_config.get('stratum', {})
                    if stratum.get('port'):
                        MULTI_COIN_NODES[symbol]['stratum_ports']['v1'] = stratum['port']
                    if stratum.get('portV2'):
                        MULTI_COIN_NODES[symbol]['stratum_ports']['v2'] = stratum['portV2']
                    if stratum.get('portTLS'):
                        MULTI_COIN_NODES[symbol]['stratum_ports']['tls'] = stratum['portTLS']

                    # Update RPC port from config
                    daemon = coin_config.get('daemon', {})
                    if daemon.get('port'):
                        MULTI_COIN_NODES[symbol]['rpc_port'] = daemon['port']

                    # Update enabled status
                    MULTI_COIN_NODES[symbol]['enabled'] = coin_config.get('enabled', False)

            print(f"Loaded stratum ports from config for {len(coins)} coin(s)")
            return True

        # Single-coin configuration (V1 config)
        stratum = config.get('stratum', {})
        listen_addr = stratum.get('listen', '')
        if listen_addr:
            # Parse port from "0.0.0.0:3333" format
            if ':' in listen_addr:
                try:
                    port = int(listen_addr.split(':')[-1])
                    # Update the primary coin's port
                    pool = config.get('pool', {})
                    coin = pool.get('coin', '').upper()
                    # Coin name to symbol mapping (exact key matching, order doesn't matter)
                    coin_map = {
                        'DIGIBYTE': 'DGB', 'DGB': 'DGB',
                        'BITCOINCASH': 'BCH', 'BITCOIN-CASH': 'BCH', 'BCH': 'BCH',
                        'BITCOINII': 'BC2', 'BITCOIN-II': 'BC2', 'BITCOIN2': 'BC2', 'BC2': 'BC2', 'BCII': 'BC2',
                        'BITCOIN': 'BTC', 'BTC': 'BTC',
                        'LITECOIN': 'LTC', 'LTC': 'LTC',
                        'DOGECOIN': 'DOGE', 'DOGE': 'DOGE',
                        'DIGIBYTE-SCRYPT': 'DGB-SCRYPT', 'DGB-SCRYPT': 'DGB-SCRYPT',
                        'NAMECOIN': 'NMC', 'NMC': 'NMC',
                        'SYSCOIN': 'SYS', 'SYS': 'SYS',
                        'MYRIADCOIN': 'XMY', 'MYRIAD': 'XMY', 'XMY': 'XMY',
                        'FRACTALBITCOIN': 'FBTC', 'FRACTAL-BITCOIN': 'FBTC', 'FBTC': 'FBTC',
                        'QBITX': 'QBX', 'Q-BITX': 'QBX', 'QBX': 'QBX',
                        'PEPECOIN': 'PEP', 'PEP': 'PEP',
                        'CATCOIN': 'CAT', 'CAT': 'CAT'
                    }
                    symbol = coin_map.get(coin, None)
                    if not symbol:
                        print(f"Warning: Unknown coin '{coin}' in config - using as-is")
                        symbol = coin
                    if symbol in MULTI_COIN_NODES:
                        MULTI_COIN_NODES[symbol]['stratum_ports']['v1'] = port
                        print(f"Loaded stratum port {port} for {symbol} from config")
                except ValueError:
                    pass

        return True
    except Exception as e:
        print(f"Could not load stratum ports from config: {e}")
        return False


def load_pool_config():
    """Load RPC credentials from pool config.yaml or blockchain daemon config files"""
    global BLOCKCHAIN_RPC_HOST, BLOCKCHAIN_RPC_PORT, BLOCKCHAIN_RPC_USER, BLOCKCHAIN_RPC_PASSWORD
    global DIGIBYTE_RPC_HOST, DIGIBYTE_RPC_PORT, DIGIBYTE_RPC_USER, DIGIBYTE_RPC_PASSWORD
    global ACTIVE_COIN_SYMBOL

    # Also load stratum ports to update MULTI_COIN_NODES
    load_stratum_ports_from_config()

    # Coin-specific config paths and default ports (all 12 supported coins)
    COIN_CONFIGS = {
        # SHA-256d coins
        "DGB": {
            "conf_path": "/spiralpool/dgb/digibyte.conf",
            "default_port": 14022,
            "name": "DigiByte"
        },
        "BTC": {
            "conf_path": "/spiralpool/btc/bitcoin.conf",
            "default_port": 8332,
            "name": "Bitcoin"
        },
        "BCH": {
            "conf_path": "/spiralpool/bch/bitcoin.conf",
            "default_port": 8432,
            "name": "Bitcoin Cash"
        },
        "BC2": {
            "conf_path": "/spiralpool/bc2/bitcoinii.conf",
            "default_port": 8339,
            "name": "Bitcoin II"
        },
        "NMC": {
            "conf_path": "/spiralpool/nmc/namecoin.conf",
            "default_port": 8336,
            "name": "Namecoin"
        },
        "SYS": {
            "conf_path": "/spiralpool/sys/syscoin.conf",
            "default_port": 8370,
            "name": "Syscoin"
        },
        "XMY": {
            "conf_path": "/spiralpool/xmy/myriadcoin.conf",
            "default_port": 10889,
            "name": "Myriad"
        },
        "FBTC": {
            "conf_path": "/spiralpool/fbtc/fractal.conf",
            "default_port": 8340,
            "name": "Fractal Bitcoin"
        },
        "QBX": {
            "conf_path": "/spiralpool/qbx/qbitx.conf",
            "default_port": 8344,
            "name": "Q-BitX"
        },
        # Scrypt coins
        "LTC": {
            "conf_path": "/spiralpool/ltc/litecoin.conf",
            "default_port": 9332,
            "name": "Litecoin"
        },
        "DOGE": {
            "conf_path": "/spiralpool/doge/dogecoin.conf",
            "default_port": 22555,
            "name": "Dogecoin"
        },
        # DGB-SCRYPT uses same config as DGB
        "DGB-SCRYPT": {
            "conf_path": "/spiralpool/dgb/digibyte.conf",
            "default_port": 14022,
            "name": "DigiByte (Scrypt)"
        },
        "PEP": {
            "conf_path": "/spiralpool/pep/pepecoin.conf",
            "default_port": 33873,
            "name": "PepeCoin"
        },
        "CAT": {
            "conf_path": "/spiralpool/cat/catcoin.conf",
            "default_port": 9932,
            "name": "Catcoin"
        }
    }

    # Method 1: Try pool config.yaml (preferred - has all settings in one place)
    try:
        import yaml
        if os.path.exists(POOL_CONFIG_PATH):
            with open(POOL_CONFIG_PATH, 'r') as f:
                config = yaml.safe_load(f)
                daemon = config.get('daemon', {})
                BLOCKCHAIN_RPC_HOST = daemon.get('host', '127.0.0.1')
                BLOCKCHAIN_RPC_PORT = daemon.get('port', 8332)
                BLOCKCHAIN_RPC_USER = daemon.get('user', '')
                BLOCKCHAIN_RPC_PASSWORD = daemon.get('password', '')

                # Try to detect coin from config (multiple formats supported)
                detected_coin = None

                # Format 1: pools[] array (multi-pool format)
                # Detection order matters: check specific coins before generic ones
                # BCH/BC2 must be checked before BTC since 'bitcoincash'/'bitcoinii' contain 'bitcoin'
                # DGB-SCRYPT must be checked before DGB
                pools = config.get('pools', [])
                if pools:
                    coin_type = pools[0].get('coin', '').lower()
                    # SHA-256d coins (check order matters)
                    if 'bitcoincash' in coin_type or 'bitcoin-cash' in coin_type or 'bch' in coin_type:
                        detected_coin = "BCH"
                    elif 'bitcoinii' in coin_type or 'bitcoin-ii' in coin_type or 'bitcoin2' in coin_type or 'bc2' in coin_type or 'bcii' in coin_type:
                        detected_coin = "BC2"
                    elif 'bitcoin' in coin_type and 'cash' not in coin_type and 'ii' not in coin_type:
                        detected_coin = "BTC"
                    elif 'digibyte-scrypt' in coin_type or 'dgb-scrypt' in coin_type or 'dgb_scrypt' in coin_type:
                        detected_coin = "DGB-SCRYPT"
                    elif 'digibyte' in coin_type or 'dgb' in coin_type:
                        detected_coin = "DGB"
                    # Scrypt coins
                    elif 'litecoin' in coin_type or 'ltc' in coin_type:
                        detected_coin = "LTC"
                    elif 'dogecoin' in coin_type or 'doge' in coin_type:
                        detected_coin = "DOGE"
                    elif 'pepecoin' in coin_type or 'pep' in coin_type:
                        detected_coin = "PEP"
                    elif 'catcoin' in coin_type or 'cat' in coin_type:
                        detected_coin = "CAT"
                    # AuxPoW coins
                    elif 'namecoin' in coin_type or 'nmc' in coin_type:
                        detected_coin = "NMC"
                    elif 'syscoin' in coin_type or 'sys' in coin_type:
                        detected_coin = "SYS"
                    elif 'myriadcoin' in coin_type or 'myriad' in coin_type or 'xmy' in coin_type:
                        detected_coin = "XMY"
                    elif 'fractal' in coin_type or 'fbtc' in coin_type:
                        detected_coin = "FBTC"
                    elif 'qbitx' in coin_type or 'q-bitx' in coin_type or 'qbx' in coin_type:
                        detected_coin = "QBX"

                # Format 2: pool.coin (single-pool format)
                if not detected_coin:
                    pool_section = config.get('pool', {})
                    coin_type = pool_section.get('coin', '').lower()
                    pool_id = pool_section.get('id', '').lower()

                    # SHA-256d coins
                    if 'bitcoincash' in coin_type or 'bitcoin-cash' in coin_type or 'bch' in coin_type or 'bch' in pool_id:
                        detected_coin = "BCH"
                    elif 'bitcoinii' in coin_type or 'bitcoin-ii' in coin_type or 'bitcoin2' in coin_type or 'bc2' in coin_type or 'bcii' in coin_type or 'bc2' in pool_id or 'bcii' in pool_id:
                        detected_coin = "BC2"
                    elif 'bitcoin' in coin_type or ('btc' in pool_id and 'bch' not in pool_id and 'bc2' not in pool_id):
                        detected_coin = "BTC"
                    elif 'digibyte-scrypt' in coin_type or 'dgb-scrypt' in coin_type or 'dgb_scrypt' in pool_id:
                        detected_coin = "DGB-SCRYPT"
                    elif 'digibyte' in coin_type or 'dgb' in coin_type or 'dgb' in pool_id:
                        detected_coin = "DGB"
                    # Scrypt coins
                    elif 'litecoin' in coin_type or 'ltc' in coin_type or 'ltc' in pool_id:
                        detected_coin = "LTC"
                    elif 'dogecoin' in coin_type or 'doge' in coin_type or 'doge' in pool_id:
                        detected_coin = "DOGE"
                    elif 'pepecoin' in coin_type or 'pep' in coin_type or 'pep' in pool_id:
                        detected_coin = "PEP"
                    elif 'catcoin' in coin_type or 'cat' in coin_type or 'cat' in pool_id:
                        detected_coin = "CAT"
                    # AuxPoW coins
                    elif 'namecoin' in coin_type or 'nmc' in coin_type or 'nmc' in pool_id:
                        detected_coin = "NMC"
                    elif 'syscoin' in coin_type or 'sys' in coin_type or 'sys' in pool_id:
                        detected_coin = "SYS"
                    elif 'myriadcoin' in coin_type or 'myriad' in coin_type or 'xmy' in coin_type or 'xmy' in pool_id:
                        detected_coin = "XMY"
                    elif 'fractal' in coin_type or 'fbtc' in coin_type or 'fbtc' in pool_id:
                        detected_coin = "FBTC"
                    elif 'qbitx' in coin_type or 'q-bitx' in coin_type or 'qbx' in coin_type or 'qbx' in pool_id:
                        detected_coin = "QBX"

                # Format 3: Fallback to daemon port
                if not detected_coin:
                    daemon_port = daemon.get('port', 0)
                    # Port-to-coin mapping for all supported coins
                    PORT_TO_COIN = {
                        8332: "BTC",
                        8339: "BC2",
                        8432: "BCH",
                        14022: "DGB",  # Also used by DGB-SCRYPT
                        9332: "LTC",
                        22555: "DOGE",
                        33873: "PEP",
                        9932: "CAT",
                        8336: "NMC",   # Namecoin RPC
                        8370: "SYS",   # Syscoin RPC
                        10889: "XMY",  # Myriadcoin RPC
                        8340: "FBTC",  # Fractal Bitcoin RPC
                        8344: "QBX",   # Q-BitX RPC
                    }
                    detected_coin = PORT_TO_COIN.get(daemon_port)
                    if not detected_coin:
                        print(f"WARNING: Unknown daemon port {daemon_port}, cannot auto-detect coin")

                ACTIVE_COIN_SYMBOL = detected_coin

                if BLOCKCHAIN_RPC_USER and BLOCKCHAIN_RPC_PASSWORD:
                    print(f"Loaded RPC credentials from {POOL_CONFIG_PATH} (coin: {ACTIVE_COIN_SYMBOL})")
                    # Update legacy aliases
                    DIGIBYTE_RPC_HOST = BLOCKCHAIN_RPC_HOST
                    DIGIBYTE_RPC_PORT = BLOCKCHAIN_RPC_PORT
                    DIGIBYTE_RPC_USER = BLOCKCHAIN_RPC_USER
                    DIGIBYTE_RPC_PASSWORD = BLOCKCHAIN_RPC_PASSWORD

                    # Also populate MULTI_COIN_NODES for the detected coin
                    if ACTIVE_COIN_SYMBOL in MULTI_COIN_NODES:
                        MULTI_COIN_NODES[ACTIVE_COIN_SYMBOL]['rpc_host'] = BLOCKCHAIN_RPC_HOST
                        MULTI_COIN_NODES[ACTIVE_COIN_SYMBOL]['rpc_port'] = BLOCKCHAIN_RPC_PORT
                        MULTI_COIN_NODES[ACTIVE_COIN_SYMBOL]['rpc_user'] = BLOCKCHAIN_RPC_USER
                        MULTI_COIN_NODES[ACTIVE_COIN_SYMBOL]['rpc_password'] = BLOCKCHAIN_RPC_PASSWORD
                        MULTI_COIN_NODES[ACTIVE_COIN_SYMBOL]['enabled'] = True

                    return True
    except Exception as e:
        print(f"Could not load pool config: {e}")

    # Method 2: Fallback to blockchain daemon config files (try each coin)
    for coin_symbol, coin_info in COIN_CONFIGS.items():
        conf_path = coin_info["conf_path"]
        try:
            if os.path.exists(conf_path):
                with open(conf_path, 'r') as f:
                    for line in f:
                        line = line.strip()
                        if line.startswith('rpcuser='):
                            BLOCKCHAIN_RPC_USER = line.split('=', 1)[1].strip().strip('"').strip("'")
                        elif line.startswith('rpcpassword='):
                            BLOCKCHAIN_RPC_PASSWORD = line.split('=', 1)[1].strip().strip('"').strip("'")
                        elif line.startswith('rpcport='):
                            BLOCKCHAIN_RPC_PORT = int(line.split('=', 1)[1].strip().strip('"').strip("'"))

                if BLOCKCHAIN_RPC_USER and BLOCKCHAIN_RPC_PASSWORD:
                    ACTIVE_COIN_SYMBOL = coin_symbol
                    BLOCKCHAIN_RPC_PORT = BLOCKCHAIN_RPC_PORT or coin_info["default_port"]
                    print(f"Loaded RPC credentials from {conf_path} ({coin_info['name']})")
                    # Update legacy aliases
                    DIGIBYTE_RPC_HOST = BLOCKCHAIN_RPC_HOST
                    DIGIBYTE_RPC_PORT = BLOCKCHAIN_RPC_PORT
                    DIGIBYTE_RPC_USER = BLOCKCHAIN_RPC_USER
                    DIGIBYTE_RPC_PASSWORD = BLOCKCHAIN_RPC_PASSWORD
                    return True
        except Exception as e:
            print(f"Could not load {conf_path}: {e}")

    # Method 3: Check environment variables
    env_user = os.environ.get('BLOCKCHAIN_RPC_USER')
    env_pass = os.environ.get('BLOCKCHAIN_RPC_PASSWORD')
    if env_user and env_pass:
        BLOCKCHAIN_RPC_USER = env_user
        BLOCKCHAIN_RPC_PASSWORD = env_pass
        DIGIBYTE_RPC_USER = env_user
        DIGIBYTE_RPC_PASSWORD = env_pass
        print("Loaded RPC credentials from environment variables")
        return True

    print("WARNING: Could not load RPC credentials. Node health will be unavailable.")
    print("  Check: /spiralpool/config/config.yaml")
    print("  Or blockchain config: dgb/digibyte.conf, btc/bitcoin.conf, bch/bitcoin.conf")
    print("  Or set environment variables: BLOCKCHAIN_RPC_USER, BLOCKCHAIN_RPC_PASSWORD")
    return False


def digibyte_rpc(method, params=None):
    """Make an RPC call to the blockchain node (works for DGB, BTC, or BCH)"""
    if not DIGIBYTE_RPC_USER or not DIGIBYTE_RPC_PASSWORD:
        load_pool_config()

    if not DIGIBYTE_RPC_USER:
        return None

    try:
        payload = {
            "jsonrpc": "1.0",
            "id": "dashboard",
            "method": method,
            "params": params or []
        }
        response = requests.post(
            f"http://{DIGIBYTE_RPC_HOST}:{DIGIBYTE_RPC_PORT}",
            json=payload,
            auth=(DIGIBYTE_RPC_USER, DIGIBYTE_RPC_PASSWORD),
            timeout=10
        )
        result = response.json()
        return result.get("result")
    except Exception as e:
        print(f"RPC error ({method}): {e}")
        return None


# ============================================
# MULTI-COIN NODE MANAGEMENT
# ============================================

# Multi-coin node configuration
# Supports 9 coins: DGB, BTC, BCH, BC2 (SHA-256d) and LTC, DOGE, DGB-SCRYPT, PEP, CAT (Scrypt)
MULTI_COIN_NODES = {
    # === SHA-256d Coins ===
    # Bitcoin II (BC2) - "nearly 1:1 re-launch of Bitcoin" with new genesis block
    # WARNING: BC2 uses identical address formats to Bitcoin (bc1q, 1, 3)
    "BC2": {
        "name": "Bitcoin II",
        "symbol": "BC2",
        "algorithm": "sha256d",
        "rpc_host": "127.0.0.1",
        "rpc_port": 8339,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/bc2",
        "config_file": "/spiralpool/bc2/bitcoinii.conf",
        "service_name": "bitcoiniid",
        "stratum_ports": {"v1": 6333, "v2": 6334, "tls": 6335},
        "block_time": 600,  # 10 minutes
        "merge_mining": None,  # Solo only
        "enabled": False
    },
    "BCH": {
        "name": "Bitcoin Cash",
        "symbol": "BCH",
        "algorithm": "sha256d",
        "rpc_host": "127.0.0.1",
        "rpc_port": 8432,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/bch",
        "config_file": "/spiralpool/bch/bitcoin.conf",
        "service_name": "bitcoind-bch",
        "stratum_ports": {"v1": 5333, "v2": 5334, "tls": 5335},
        "block_time": 600,  # 10 minutes
        "merge_mining": None,  # Solo only
        "enabled": False
    },
    "BTC": {
        "name": "Bitcoin",
        "symbol": "BTC",
        "algorithm": "sha256d",
        "rpc_host": "127.0.0.1",
        "rpc_port": 8332,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/btc",
        "config_file": "/spiralpool/btc/bitcoin.conf",
        "service_name": "bitcoind",
        "stratum_ports": {"v1": 4333, "v2": 4334, "tls": 4335},
        "block_time": 600,  # 10 minutes
        "merge_mining": {"role": "parent", "aux_chains": ["NMC", "SYS", "XMY", "FBTC"]},
        "enabled": False
    },
    # Namecoin - first coin to implement AuxPoW (merge-mined with Bitcoin)
    "NMC": {
        "name": "Namecoin",
        "symbol": "NMC",
        "algorithm": "sha256d",
        "rpc_host": "127.0.0.1",
        "rpc_port": 8336,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/nmc",
        "config_file": "/spiralpool/nmc/namecoin.conf",
        "service_name": "namecoind",
        "stratum_ports": {"v1": 14335, "v2": 14336, "tls": 14337},
        "block_time": 600,  # 10 minutes (same as Bitcoin)
        "merge_mining": {"role": "auxiliary", "parent_chain": "BTC", "chain_id": 1},
        "enabled": False
    },
    # Syscoin - UTXO platform with AuxPoW (merge-mined with Bitcoin)
    # MERGE-ONLY: Cannot solo mine (CbTx/quorum commitment not supported)
    "SYS": {
        "name": "Syscoin",
        "symbol": "SYS",
        "algorithm": "sha256d",
        "rpc_host": "127.0.0.1",
        "rpc_port": 8370,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/sys",
        "config_file": "/spiralpool/sys/syscoin.conf",
        "service_name": "syscoind",
        "stratum_ports": {"v1": 15335, "v2": 15336, "tls": 15337},
        "block_time": 60,  # 1 minute
        "merge_mining": {"role": "auxiliary", "parent_chain": "BTC", "chain_id": 16, "merge_only": True},
        "enabled": False
    },
    # Myriad - Multi-algo coin with SHA256d AuxPoW (merge-mined with Bitcoin)
    "XMY": {
        "name": "Myriad",
        "symbol": "XMY",
        "algorithm": "sha256d",
        "rpc_host": "127.0.0.1",
        "rpc_port": 10889,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/xmy",
        "config_file": "/spiralpool/xmy/myriadcoin.conf",
        "service_name": "myriadcoind",
        "stratum_ports": {"v1": 17335, "v2": 17336, "tls": 17337},
        "block_time": 60,  # 1 minute
        "merge_mining": {"role": "auxiliary", "parent_chain": "BTC", "chain_id": 90},
        "enabled": False
    },
    # Fractal Bitcoin - Bitcoin scaling solution with AuxPoW (merge-mined with Bitcoin)
    # Uses 30-second blocks and Cadence Mining (2 permissionless + 1 merged per 3 blocks)
    "FBTC": {
        "name": "Fractal Bitcoin",
        "symbol": "FBTC",
        "algorithm": "sha256d",
        "rpc_host": "127.0.0.1",
        "rpc_port": 8340,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/fbtc",
        "config_file": "/spiralpool/fbtc/fractal.conf",
        "service_name": "fractald",
        "stratum_ports": {"v1": 18335, "v2": 18336, "tls": 18337},
        "block_time": 30,  # 30 seconds (NOT 600 like Bitcoin!)
        "merge_mining": {"role": "auxiliary", "parent_chain": "BTC", "chain_id": 8228},
        "enabled": False
    },
    # Q-BitX - SHA-256d standalone coin (NOT merge-mineable)
    "QBX": {
        "name": "Q-BitX",
        "symbol": "QBX",
        "algorithm": "sha256d",
        "rpc_host": "127.0.0.1",
        "rpc_port": 8344,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/qbx",
        "config_file": "/spiralpool/qbx/qbitx.conf",
        "service_name": "qbitxd",
        "stratum_ports": {"v1": 20335, "v2": 20336, "tls": 20337},
        "block_time": 150,  # 2.5 minutes
        "merge_mining": None,  # Standalone, NOT merge-mineable
        "enabled": False
    },
    # === Scrypt Coins ===
    # Catcoin - first cat-themed memecoin
    "CAT": {
        "name": "Catcoin",
        "symbol": "CAT",
        "algorithm": "scrypt",
        "rpc_host": "127.0.0.1",
        "rpc_port": 9932,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/cat",
        "config_file": "/spiralpool/cat/catcoin.conf",
        "service_name": "catcoind",
        "stratum_ports": {"v1": 12335, "v2": 12336, "tls": 12337},
        "block_time": 600,  # 10 minutes (like Bitcoin)
        "merge_mining": None,  # Solo only
        "enabled": False
    },
    "DGB": {
        "name": "DigiByte",
        "symbol": "DGB",
        "algorithm": "sha256d",
        "rpc_host": "127.0.0.1",
        "rpc_port": 14022,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/dgb",
        "config_file": "/spiralpool/dgb/digibyte.conf",
        "service_name": "digibyted",
        "stratum_ports": {"v1": 3333, "v2": 3334, "tls": 3335},
        "block_time": 15,  # seconds
        "merge_mining": None,  # Solo only
        "enabled": False  # No default - must be explicitly configured
    },
    # DigiByte Scrypt - uses same blockchain/node as DGB but different mining algorithm
    "DGB-SCRYPT": {
        "name": "DigiByte (Scrypt)",
        "symbol": "DGB-SCRYPT",
        "algorithm": "scrypt",
        "rpc_host": "127.0.0.1",
        "rpc_port": 14022,  # Same as DGB - shares node
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/dgb",  # Same as DGB
        "config_file": "/spiralpool/dgb/digibyte.conf",  # Same as DGB
        "service_name": "digibyted",  # Same as DGB
        "stratum_ports": {"v1": 3336, "v2": 3337, "tls": 3338},
        "block_time": 15,  # Same as DGB SHA256d
        "merge_mining": None,  # Solo only
        "enabled": False
    },
    "DOGE": {
        "name": "Dogecoin",
        "symbol": "DOGE",
        "algorithm": "scrypt",
        "rpc_host": "127.0.0.1",
        "rpc_port": 22555,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/doge",
        "config_file": "/spiralpool/doge/dogecoin.conf",
        "service_name": "dogecoind",
        "stratum_ports": {"v1": 8335, "v2": 8337, "tls": 8342},
        "block_time": 60,  # 1 minute
        "merge_mining": {"role": "auxiliary", "parent_chain": "LTC", "chain_id": 98},
        "enabled": False
    },
    "LTC": {
        "name": "Litecoin",
        "symbol": "LTC",
        "algorithm": "scrypt",
        "rpc_host": "127.0.0.1",
        "rpc_port": 9332,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/ltc",
        "config_file": "/spiralpool/ltc/litecoin.conf",
        "service_name": "litecoind",
        "stratum_ports": {"v1": 7333, "v2": 7334, "tls": 7335},
        "block_time": 150,  # 2.5 minutes
        "merge_mining": {"role": "parent", "aux_chains": ["DOGE", "PEP"]},
        "enabled": False
    },
    # PepeCoin - Scrypt fork merge-mined with Litecoin
    "PEP": {
        "name": "PepeCoin",
        "symbol": "PEP",
        "algorithm": "scrypt",
        "rpc_host": "127.0.0.1",
        "rpc_port": 33873,
        "rpc_user": "",
        "rpc_password": "",
        "data_dir": "/spiralpool/pep",
        "config_file": "/spiralpool/pep/pepecoin.conf",
        "service_name": "pepecoind",
        "stratum_ports": {"v1": 10335, "v2": 10336, "tls": 10337},
        "block_time": 60,  # 1 minute
        "merge_mining": {"role": "auxiliary", "parent_chain": "LTC", "chain_id": 63},
        "enabled": False
    },
}

# Cache for multi-coin node health
multi_coin_health_cache = {
    "last_update": 0,
    "nodes": {}
}

# Active coin tracking (auto-detected from pool config)
# NOTE: No default coin - must be detected from config to avoid showing wrong coin
active_coins = {
    "primary": None,  # Will be detected from pool config
    "enabled": [],    # Will be populated from pool config
    "multi_coin_mode": False,  # True if multi-coin mode is enabled
    "last_update": 0
}

# CoinGecko coin ID mapping (for all supported coins)
COINGECKO_IDS = {
    # SHA-256d coins
    "DGB": "digibyte",
    "BTC": "bitcoin",
    "BCH": "bitcoin-cash",
    "BC2": "bitcoinii",  # Bitcoin II CoinGecko ID (confirmed listed)
    "NMC": "namecoin",   # Namecoin - first AuxPoW coin
    "SYS": "syscoin",    # Syscoin - UTXO platform with AuxPoW
    "XMY": "myriadcoin", # Myriad - Multi-algo coin
    "FBTC": "fractal-bitcoin",  # Fractal Bitcoin - Bitcoin scaling with AuxPoW
    "QBX": None,  # Q-BitX - not listed on CoinGecko
    # Scrypt coins
    "LTC": "litecoin",
    "DOGE": "dogecoin",
    "DGB-SCRYPT": "digibyte",  # Same as DGB - same blockchain
    "PEP": "pepecoin",  # PepeCoin Scrypt fork
    "CAT": "catcoin"    # Catcoin (the 2013 one, not BEP20)
}

# Block explorer URLs per coin with fallback support
# Each coin has a primary and list of fallback explorers
BLOCK_EXPLORERS = {
    # === SHA-256d Coins ===
    "DGB": {
        "api": "https://digiexplorer.info/api",
        "url": "https://digiexplorer.info",
        "name": "DigiExplorer",
        "fallbacks": [
            {"api": "https://chainz.cryptoid.info/dgb/api.dws", "url": "https://chainz.cryptoid.info/dgb", "name": "Chainz"},
        ]
    },
    "BTC": {
        "api": "https://blockchain.info",
        "url": "https://blockchain.info",
        "name": "Blockchain.info",
        "fallbacks": [
            {"api": "https://api.blockchair.com/bitcoin", "url": "https://blockchair.com/bitcoin", "name": "Blockchair"},
            {"api": "https://mempool.space/api", "url": "https://mempool.space", "name": "Mempool.space"},
        ]
    },
    "BCH": {
        "api": "https://api.blockchair.com/bitcoin-cash",
        "url": "https://blockchair.com/bitcoin-cash",
        "name": "Blockchair",
        "fallbacks": [
            {"api": "https://rest.bitcoin.com/v2", "url": "https://explorer.bitcoin.com/bch", "name": "Bitcoin.com"},
        ]
    },
    "BC2": {
        "api": None,  # No public API yet - use RPC for block data
        "url": "https://bitcoin-ii.org",  # Official website
        "name": "Bitcoin II Explorer",
        "fallbacks": []  # New chain - limited explorer options
    },
    "NMC": {
        "api": "https://namecha.in/api",
        "url": "https://namecha.in",
        "name": "Namecha.in",
        "fallbacks": [
            {"api": "https://chainz.cryptoid.info/nmc/api.dws", "url": "https://chainz.cryptoid.info/nmc", "name": "Chainz"},
        ]
    },
    "SYS": {
        "api": "https://api.blockchair.com/syscoin",
        "url": "https://blockchair.com/syscoin",
        "name": "Blockchair",
        "fallbacks": [
            {"api": "https://chainz.cryptoid.info/sys/api.dws", "url": "https://chainz.cryptoid.info/sys", "name": "Chainz"},
        ]
    },
    "XMY": {
        "api": "https://chainz.cryptoid.info/xmy/api.dws",
        "url": "https://chainz.cryptoid.info/xmy",
        "name": "Chainz",
        "fallbacks": []
    },
    "FBTC": {
        "api": "https://mempool.fractalbitcoin.io/api",
        "url": "https://mempool.fractalbitcoin.io",
        "name": "Fractal Mempool",
        "fallbacks": [
            {"api": None, "url": "https://fractal.unisat.io/explorer", "name": "UniSat Explorer"},
        ]
    },
    "QBX": {
        "api": None,  # No public REST API — use RPC for block data
        "url": "https://explorer.qbitx.org",
        "name": "Q-BitX Explorer",
        "fallbacks": []
    },
    # === Scrypt Coins ===
    "LTC": {
        "api": "https://api.blockchair.com/litecoin",
        "url": "https://blockchair.com/litecoin",
        "name": "Blockchair",
        "fallbacks": [
            {"api": "https://chainz.cryptoid.info/ltc/api.dws", "url": "https://chainz.cryptoid.info/ltc", "name": "Chainz"},
        ]
    },
    "DOGE": {
        "api": "https://api.blockchair.com/dogecoin",
        "url": "https://blockchair.com/dogecoin",
        "name": "Blockchair",
        "fallbacks": [
            {"api": "https://chainz.cryptoid.info/doge/api.dws", "url": "https://chainz.cryptoid.info/doge", "name": "Chainz"},
        ]
    },
    # DGB-SCRYPT uses same explorer as DGB (same blockchain)
    "DGB-SCRYPT": {
        "api": "https://digiexplorer.info/api",
        "url": "https://digiexplorer.info",
        "name": "DigiExplorer",
        "fallbacks": [
            {"api": "https://chainz.cryptoid.info/dgb/api.dws", "url": "https://chainz.cryptoid.info/dgb", "name": "Chainz"},
        ]
    },
    # === Additional Scrypt Meme Coins ===
    "PEP": {
        "api": None,  # Use RPC for block data - limited public APIs
        "url": "https://pepecoin.org",
        "name": "Pepecoin Explorer",
        "fallbacks": []
    },
    "CAT": {
        "api": None,  # Use RPC for block data - limited public APIs
        "url": "https://www.catcoin2013.org",
        "name": "Catcoin Explorer",
        "fallbacks": []
    },
}

# Legacy explorer constants - kept for backwards compatibility with pre-multi-coin functions
# (fetch_block_details, fetch_transaction_details, fetch_address_transactions, block history enrichment)
# Preferred path for new code: use BLOCK_EXPLORERS dict above
DIGIEXPLORER_API = "https://digiexplorer.info/api"
DIGIEXPLORER_URL = "https://digiexplorer.info"


def fetch_with_explorer_fallback(coin: str, endpoint: str, timeout: float = TIMEOUT_SLOW) -> tuple:
    """
    Fetch data from block explorer API with automatic fallback to alternate explorers.

    Args:
        coin: Coin symbol (DGB, BTC, BCH)
        endpoint: API endpoint path (e.g., "/block/123" or "?q=getblockcount")
        timeout: Request timeout in seconds

    Returns:
        tuple: (response_data, success, explorer_name_used)
    """
    if coin not in BLOCK_EXPLORERS:
        return (None, False, None)

    explorer = BLOCK_EXPLORERS[coin]
    explorers_to_try = [{"api": explorer["api"], "url": explorer["url"], "name": explorer["name"]}]
    explorers_to_try.extend(explorer.get("fallbacks", []))

    for exp in explorers_to_try:
        api_url = exp["api"]
        # Handle different URL formats
        if endpoint.startswith("?"):
            full_url = f"{api_url}{endpoint}"
        else:
            full_url = f"{api_url}{endpoint}"

        data, success, error = robust_request("get", full_url, timeout=timeout, max_retries=1)

        if success:
            return (data, True, exp["name"])

        print(f"Explorer fallback: {exp['name']} failed for {coin} - {error}")

    return (None, False, None)

# Block reward defaults per coin
COIN_BLOCK_REWARDS = {
    # SHA-256d coins
    "DGB": 277.38,      # DigiByte SHA256 block reward (Dec 2025, from whattomine.com)
    "BTC": 3.125,       # Bitcoin block reward after 2024 halving
    "BCH": 3.125,       # Bitcoin Cash block reward
    "BC2": 50.0,        # Bitcoin II block reward (same as original Bitcoin, started Dec 2024)
    # SHA-256d merge-mineable coins
    "NMC": 6.25,        # Namecoin block reward (after multiple halvings)
    "SYS": 1.25,        # Syscoin block reward (approximate current)
    "XMY": 500,         # Myriad block reward per algo (approximate)
    "FBTC": 25,         # Fractal Bitcoin block reward (25 FB per block)
    "QBX": 12.5,        # Q-BitX block reward (12.5 QBX per block)
    # Scrypt coins
    "LTC": 6.25,        # Litecoin block reward after 2023 halving
    "DOGE": 10000,      # Dogecoin block reward (fixed at 10,000)
    "DGB-SCRYPT": 277.38,  # Same as DGB SHA256d (shared node)
    "PEP": 50,          # PepeCoin block reward (estimated)
    "CAT": 25           # Catcoin block reward (after halving)
}

# Block time in seconds per coin (for earnings calculation)
COIN_BLOCK_TIMES = {
    # SHA-256d coins
    "DGB": 15,          # DigiByte SHA256d block time (15 seconds)
    "BTC": 600,         # Bitcoin 10-minute blocks
    "BCH": 600,         # Bitcoin Cash 10-minute blocks
    "BC2": 600,         # Bitcoin II 10-minute blocks (same as Bitcoin)
    # SHA-256d merge-mineable coins
    "NMC": 600,         # Namecoin 10-minute blocks (same as Bitcoin)
    "SYS": 60,          # Syscoin 1-minute blocks
    "XMY": 60,          # Myriad 1-minute blocks per algo
    "FBTC": 30,         # Fractal Bitcoin 30-second blocks (NOT 600 like Bitcoin!)
    "QBX": 150,         # Q-BitX 2.5-minute blocks
    # Scrypt coins
    "LTC": 150,         # Litecoin 2.5-minute blocks
    "DOGE": 60,         # Dogecoin 1-minute blocks
    "DGB-SCRYPT": 15,   # Same as DGB SHA256d (shared node)
    "PEP": 60,          # PepeCoin 1-minute blocks
    "CAT": 600          # Catcoin 10-minute blocks (like Bitcoin)
}


def get_algorithm_for_coin(coin_symbol):
    """Get the mining algorithm (sha256d or scrypt) for a given coin symbol.

    Uses MULTI_COIN_NODES as the single source of truth for algorithm assignments.
    Falls back to 'sha256d' for unknown coins.
    """
    if coin_symbol and coin_symbol.upper() in MULTI_COIN_NODES:
        return MULTI_COIN_NODES[coin_symbol.upper()].get('algorithm', 'sha256d')
    # Fallback for backwards compatibility
    scrypt_coins = ['LTC', 'DOGE', 'DGB-SCRYPT', 'PEP', 'CAT']
    return 'scrypt' if coin_symbol in scrypt_coins else 'sha256d'


def get_algorithm_for_port(port):
    """Determine algorithm and merge mining status from stratum port number.

    Returns a dict with 'algorithm' ('sha256d'/'scrypt') and 'merge_mining' (bool),
    or None if the port doesn't match any enabled coin.
    """
    for symbol, node in MULTI_COIN_NODES.items():
        if not node.get('enabled'):
            continue
        ports = node.get('stratum_ports', {})
        if port in (ports.get('v1'), ports.get('v2'), ports.get('tls')):
            algo = node.get('algorithm', 'sha256d')
            mm = node.get('merge_mining') or {}
            is_merge_parent = mm.get('role') == 'parent' and bool(mm.get('aux_chains'))
            return {'algorithm': algo, 'merge_mining': is_merge_parent}
    return None  # unknown port


def get_primary_coin_algo_info():
    """Get algo_info dict for the primary enabled coin (fallback for miners without port detection).

    Returns a dict with 'algorithm' and 'merge_mining', or None if no coin is enabled.
    """
    for symbol, node in MULTI_COIN_NODES.items():
        if not node.get('enabled'):
            continue
        # Skip auxiliary-only coins — look for parent or solo coins
        mm = node.get('merge_mining') or {}
        if mm.get('role') == 'auxiliary':
            continue
        algo = node.get('algorithm', 'sha256d')
        is_merge_parent = mm.get('role') == 'parent' and bool(mm.get('aux_chains'))
        return {'algorithm': algo, 'merge_mining': is_merge_parent}
    # If only aux coins found, return first enabled coin's algo
    for symbol, node in MULTI_COIN_NODES.items():
        if node.get('enabled'):
            return {'algorithm': node.get('algorithm', 'sha256d'), 'merge_mining': False}
    return None


def _extract_port_from_url(pool_url):
    """Extract the port number from a pool URL string. Returns int or None."""
    if not pool_url:
        return None
    url = pool_url
    # Strip protocol prefix
    for prefix in ['stratum+tcp://', 'stratum+ssl://', 'stratum://', 'tcp://', 'ssl://']:
        if url.lower().startswith(prefix):
            url = url[len(prefix):]
            break
    if ':' in url:
        try:
            return int(url.rsplit(':', 1)[1].split('/')[0])
        except (ValueError, IndexError):
            pass
    return None


def get_miner_algorithm(miner_data):
    """Determine a miner's algorithm and merge mining status.

    Priority:
    1. Extract port from pool_url → look up in MULTI_COIN_NODES
    2. If miner is online, fall back to primary coin's algorithm

    Returns a dict with 'algorithm' and 'merge_mining', or None if undetermined.
    """
    # Try port-based detection from pool_url
    pool_url = miner_data.get("pool_url", "")
    port = _extract_port_from_url(pool_url)
    if port is not None:
        result = get_algorithm_for_port(port)
        if result:
            return result

    # Fallback: if miner is online but port detection failed, infer from device type
    if miner_data.get("online"):
        miner_type = miner_data.get("type", "").lower()
        # ESP32 miners only support SHA-256d (hardware limitation)
        if "esp32" in miner_type:
            return {'algorithm': 'sha256d', 'merge_mining': False}
        # All other miners: use primary coin's algorithm
        return get_primary_coin_algo_info()

    return None


# ============================================
# DEVICE GROUP MAPPING
# ============================================
# Maps miner type strings (from fetch_all_miners) to display groups.
# Uses keyword-based matching (checked in order, first match wins).
# Covers all 24+ device types including dynamic model-based types.

DEVICE_TYPE_GROUP_RULES = [
    # Scrypt-specific devices (must check before generic matches)
    (["hammer"], "Hammer (Scrypt)"),
    (["antminer scrypt", "antminer_scrypt"], "Antminer Scrypt"),
    (["elphapex"], "Elphapex (Scrypt)"),
    # SHA-256 ASIC firmware variants (must check BEFORE generic antminer)
    (["braiins", "braiinos", "bos+", "bos "], "Antminer SHA-256"),
    (["vnish"], "Antminer SHA-256"),
    (["luxos"], "Antminer SHA-256"),
    # Industrial ASICs
    (["antminer"], "Antminer SHA-256"),
    (["whatsminer"], "Whatsminer"),
    (["avalon", "canaan", "avalonminer"], "Avalon"),
    (["goldshell"], "Goldshell"),
    # Small/home miners - AxeOS family (dedicated ASIC boards running AxeOS firmware)
    (["nerdqaxe", "nerdaxe", "nerdoctaxe"], "NerdQAxe"),
    (["axeos", "bitaxe"], "AxeOS"),
    (["nmaxe", "nmax"], "AxeOS"),
    (["qaxe"], "AxeOS"),
    (["lucky miner", "luckyminer"], "AxeOS"),
    (["jingle miner", "jingleminer"], "AxeOS"),
    (["zyber", "tinychip"], "AxeOS"),
    # ESP32-based miners (generic ESP32 software miners, no HTTP API)
    (["esp32"], "ESP32"),
    # Other ASICs
    (["gekkoscience", "gekko"], "Other ASIC"),
    (["ipollo"], "Other ASIC"),
    (["ebang", "ebit"], "Other ASIC"),
    (["epic", "blockminer"], "Other ASIC"),
    (["futurebit", "apollo"], "Other ASIC"),
    (["innosilicon"], "Other ASIC"),
]

# Display order (lower = higher in list)
# All groups get algo-suffixed at runtime based on the stratum port miners connect to.
# Groups whose name already contains the algorithm (e.g. "Antminer SHA-256") keep their name.
DEVICE_GROUP_ORDER = {
    # SHA-256d solo
    "Antminer SHA-256": 0,
    "Whatsminer (SHA-256d)": 1,
    "Avalon (SHA-256d)": 2,
    "NerdQAxe (SHA-256d)": 3,
    "AxeOS (SHA-256d)": 4,
    # SHA-256d merge mining
    "Antminer SHA-256 + Merge Mining": 5,
    "Whatsminer (SHA-256d + Merge Mining)": 5,
    "Avalon (SHA-256d + Merge Mining)": 5,
    "NerdQAxe (SHA-256d + Merge Mining)": 5,
    "AxeOS (SHA-256d + Merge Mining)": 6,
    # Scrypt solo
    "Antminer Scrypt": 7,
    "Hammer (Scrypt)": 8,
    "Elphapex (Scrypt)": 8,
    "NerdQAxe (Scrypt)": 9,
    "AxeOS (Scrypt)": 10,
    "Whatsminer (Scrypt)": 10,
    "Avalon (Scrypt)": 10,
    # Scrypt merge mining
    "NerdQAxe (Scrypt + Merge Mining)": 11,
    "AxeOS (Scrypt + Merge Mining)": 12,
    # Other devices
    "Goldshell (SHA-256d)": 13,
    "Goldshell (SHA-256d + Merge Mining)": 14,
    "Goldshell (Scrypt)": 15,
    "Goldshell (Scrypt + Merge Mining)": 16,
    "ESP32 (SHA-256d)": 17,
    "ESP32 (SHA-256d + Merge Mining)": 17,
    "ESP32 (Scrypt)": 18,
    "ESP32 (Scrypt + Merge Mining)": 18,
    "Other ASIC (SHA-256d)": 19,
    "Other ASIC (SHA-256d + Merge Mining)": 19,
    "Other ASIC (Scrypt)": 20,
    "Other ASIC (Scrypt + Merge Mining)": 20,
    "Unknown (SHA-256d)": 21,
    "Unknown (SHA-256d + Merge Mining)": 21,
    "Unknown (Scrypt)": 22,
    "Unknown (Scrypt + Merge Mining)": 22,
    # Fallbacks for offline miners / port detection failure (no algo suffix)
    "Whatsminer": 1,
    "Avalon": 2,
    "NerdQAxe": 3,
    "AxeOS": 4,
    "Goldshell": 13,
    "ESP32": 17,
    "Other ASIC": 19,
    "Unknown": 21,
}

# Groups whose name already contains the algorithm — these don't get a suffix
# because adding one would be redundant (e.g., "Antminer SHA-256 (SHA-256d)")
_ALGO_IN_NAME_GROUPS = {"Antminer SHA-256", "Antminer Scrypt", "Hammer (Scrypt)", "Elphapex (Scrypt)"}

# Suffix map: (algorithm, merge_mining) → display suffix
_ALGO_SUFFIX = {
    ("sha256d", False): " (SHA-256d)",
    ("sha256d", True): " (SHA-256d + Merge Mining)",
    ("scrypt", False): " (Scrypt)",
    ("scrypt", True): " (Scrypt + Merge Mining)",
}

# Separate suffix for groups that already have algo in name (only adds merge mining)
_MERGE_ONLY_SUFFIX = {
    ("sha256d", True): " + Merge Mining",
    ("scrypt", True): " + Merge Mining",
}


def get_device_group(miner_type, algo_info=None):
    """Map a miner type string to its display group name with algorithm suffix.

    Uses keyword matching against DEVICE_TYPE_GROUP_RULES (first match wins).
    ALL groups get an algorithm suffix based on the miner's connected stratum port:
    - Groups that don't mention algo: get full suffix e.g. " (SHA-256d)"
    - Groups that already mention algo: only get " + Merge Mining" if applicable
    - If algo_info is None (offline/unknown): no suffix added

    Args:
        miner_type: The miner's type string (e.g., "AxeOS", "NerdQAxe++", "Avalon0")
        algo_info: Dict with 'algorithm' and 'merge_mining' keys, or None
    """
    if not miner_type:
        base = "Unknown"
    else:
        base = "Unknown"
        type_lower = miner_type.lower()
        for keywords, group_name in DEVICE_TYPE_GROUP_RULES:
            for kw in keywords:
                if kw in type_lower:
                    base = group_name
                    break
            if base != "Unknown":
                break

    if not algo_info:
        return base

    algo = algo_info.get('algorithm')
    mm = algo_info.get('merge_mining', False)

    # Groups whose name already contains the algorithm — only add merge mining suffix
    if base in _ALGO_IN_NAME_GROUPS:
        suffix = _MERGE_ONLY_SUFFIX.get((algo, mm))
        if suffix:
            return base + suffix
        return base

    # All other groups get the full algo suffix
    suffix = _ALGO_SUFFIX.get((algo, mm))
    if suffix:
        return base + suffix
    return base


def get_device_group_algorithm(group_name):
    """Determine the algorithm for a device group from its name."""
    # Check for algo keywords in group name (covers both suffixed and inherent names)
    name_lower = group_name.lower()
    if "sha-256" in name_lower or "sha256" in name_lower:
        return "sha256d"
    if "scrypt" in name_lower:
        return "scrypt"
    return "unknown"


def fetch_live_block_reward_dgb():
    """Fetch live DGB block reward from whattomine.com API with fallback."""
    block_height = 0
    block_reward = None  # Will be set from API or config fallback

    try:
        # Primary: Try whattomine.com API for accurate block reward
        response = requests.get(
            "https://whattomine.com/coins/113.json",  # DGB-SHA256 coin ID
            timeout=10,
            headers={"User-Agent": "SpiralPool-Dashboard/1.0"}
        )
        if response.status_code == 200:
            data = response.json()
            if "block_reward" in data:
                block_reward = float(data["block_reward"])
            if "last_block" in data:
                block_height = int(data["last_block"])
            return {
                "block_height": block_height,
                "block_reward": block_reward,
                "symbol": "DGB"
            }
    except Exception as e:
        print(f"WhatToMine API error: {e}")

    # Fallback: Get block height from chainz if whattomine failed
    try:
        response = requests.get(
            "https://chainz.cryptoid.info/dgb/api.dws?q=getblockcount",
            timeout=10,
            headers={"User-Agent": "SpiralPool-Dashboard/1.0"}
        )
        if response.status_code == 200:
            block_height = int(response.text.strip())
    except Exception as e:
        print(f"Chainz API error: {e}")
        # Final fallback: Try DigiExplorer
        try:
            response = requests.get(
                "https://digiexplorer.info/api/getblockcount",
                timeout=10,
                headers={"User-Agent": "SpiralPool-Dashboard/1.0"}
            )
            block_height = int(response.text.strip())
        except Exception as e:
            print(f"DigiExplorer API error: {e}")

    # Final fallback: use configured block reward if all APIs failed
    if block_reward is None:
        block_reward = COIN_BLOCK_REWARDS.get("DGB", 277.38)
        print("All APIs failed - using configured DGB block reward fallback")

    return {
        "block_height": block_height,
        "block_reward": block_reward,
        "symbol": "DGB"
    }


def fetch_live_block_reward_btc():
    """Fetch live BTC block reward with halving calculations."""
    try:
        # Get current block height from blockchain.info
        response = requests.get(
            "https://blockchain.info/q/getblockcount",
            timeout=10,
            headers={"User-Agent": "SpiralPool-Dashboard/1.0"}
        )
        block_height = int(response.text.strip())

        # BTC halving every 210,000 blocks, started at 50 BTC
        halvings = block_height // 210000
        block_reward = 50 / (2 ** halvings)

        return {
            "block_height": block_height,
            "block_reward": round(block_reward, 8),
            "symbol": "BTC"
        }
    except Exception as e:
        print(f"Error fetching BTC block reward: {e}")
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS["BTC"], "symbol": "BTC"}


def fetch_live_block_reward_bch():
    """Fetch live BCH block reward with halving calculations."""
    try:
        # Get current block height from blockchair
        response = requests.get(
            "https://api.blockchair.com/bitcoin-cash/stats",
            timeout=10,
            headers={"User-Agent": "SpiralPool-Dashboard/1.0"}
        )
        data = response.json()
        block_height = data.get("data", {}).get("blocks", 870000)

        # BCH halving every 210,000 blocks, started at 50 BCH
        halvings = block_height // 210000
        block_reward = 50 / (2 ** halvings)

        return {
            "block_height": block_height,
            "block_reward": round(block_reward, 8),
            "symbol": "BCH"
        }
    except Exception as e:
        print(f"Error fetching BCH block reward: {e}")
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS["BCH"], "symbol": "BCH"}


def fetch_live_block_reward_bc2():
    """Fetch live BC2 block reward with halving calculations.

    Bitcoin II uses the same halving schedule as Bitcoin:
    - 50 BC2 initial reward
    - Halving every 210,000 blocks
    - Started December 2024 with new genesis block
    """
    try:
        # Get block height from RPC (no public block explorer API yet)
        node = MULTI_COIN_NODES.get("BC2", {})
        if node.get("rpc_user") and node.get("rpc_password"):
            info = coin_rpc("BC2", "getblockchaininfo")
            if info:
                block_height = info.get("blocks", 0)
                # BC2 halving every 210,000 blocks, started at 50 BC2
                halvings = block_height // 210000
                block_reward = 50 / (2 ** halvings)
                return {
                    "block_height": block_height,
                    "block_reward": round(block_reward, 8),
                    "symbol": "BC2"
                }
    except Exception as e:
        print(f"Error fetching BC2 block reward: {e}")

    # Fallback to default (BC2 is new, started Dec 2024, still on 50 BC2 reward)
    return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS["BC2"], "symbol": "BC2"}


def fetch_live_block_reward(coin):
    """Fetch live block reward for any supported coin.

    Supports 14 coins: DGB, BTC, BCH, BC2, NMC, SYS, XMY, FBTC, QBX, LTC, DOGE, DGB-SCRYPT, PEP, CAT.
    """
    coin = coin.upper()
    # SHA-256d coins with live API lookup
    if coin == "DGB":
        return fetch_live_block_reward_dgb()
    elif coin == "BTC":
        return fetch_live_block_reward_btc()
    elif coin == "BCH":
        return fetch_live_block_reward_bch()
    elif coin == "BC2":
        return fetch_live_block_reward_bc2()
    # Scrypt coins - use static block rewards (these coins have simpler reward schedules)
    elif coin == "LTC":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("LTC", 6.25), "symbol": "LTC"}
    elif coin == "DOGE":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("DOGE", 10000), "symbol": "DOGE"}
    elif coin == "DGB-SCRYPT":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("DGB-SCRYPT", 277.38), "symbol": "DGB-SCRYPT"}
    elif coin == "PEP":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("PEP", 50), "symbol": "PEP"}
    elif coin == "CAT":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("CAT", 25), "symbol": "CAT"}
    # Aux coins - use static block rewards
    elif coin == "NMC":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("NMC", 6.25), "symbol": "NMC"}
    elif coin == "SYS":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("SYS", 7.28), "symbol": "SYS"}
    elif coin == "XMY":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("XMY", 250), "symbol": "XMY"}
    elif coin == "FBTC":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("FBTC", 25), "symbol": "FBTC"}
    elif coin == "QBX":
        return {"block_height": 0, "block_reward": COIN_BLOCK_REWARDS.get("QBX", 12.5), "symbol": "QBX"}
    else:
        return {"block_height": 0, "block_reward": 0, "symbol": coin}


def get_enabled_coins():
    """Get list of enabled coins from pool configuration.

    Detects both solo-coin mode (single coin like BTC, BCH, or DGB)
    and multi-coin mode (DGB+BTC, DGB+BCH, BTC+BCH, or all three).
    """
    global active_coins

    # Cache for 60 seconds
    if time.time() - active_coins["last_update"] < 60:
        return active_coins

    enabled = []
    primary = None
    multi_coin_mode = False

    # Method 1: Check POOL_ID environment variable for solo mode detection
    # Pool IDs like "btc-sha256-1", "bch-sha256-1", "dgb-sha256-1", "bc2-sha256-1" indicate the coin
    pool_id = os.environ.get("POOL_ID", "")
    if pool_id:
        pool_id_lower = pool_id.lower()
        # SHA-256d coins
        if pool_id_lower.startswith("btc"):
            primary = "BTC"
        elif pool_id_lower.startswith("bch"):
            primary = "BCH"
        elif pool_id_lower.startswith("bc2") or pool_id_lower.startswith("bitcoinii"):
            primary = "BC2"
        # Scrypt coins (check dgb-scrypt/dgb_scrypt before dgb)
        elif pool_id_lower.startswith("dgb-scrypt") or pool_id_lower.startswith("dgb_scrypt"):
            primary = "DGB-SCRYPT"
        elif pool_id_lower.startswith("dgb"):
            primary = "DGB"
        elif pool_id_lower.startswith("ltc"):
            primary = "LTC"
        elif pool_id_lower.startswith("doge"):
            primary = "DOGE"
        elif pool_id_lower.startswith("pep"):
            primary = "PEP"
        elif pool_id_lower.startswith("cat"):
            primary = "CAT"
        # AuxPoW coins
        elif "nmc" in pool_id_lower or "namecoin" in pool_id_lower:
            primary = "NMC"
        elif "sys" in pool_id_lower or "syscoin" in pool_id_lower:
            primary = "SYS"
        elif "xmy" in pool_id_lower or "myriad" in pool_id_lower:
            primary = "XMY"
        elif "fbtc" in pool_id_lower or "fractal" in pool_id_lower:
            primary = "FBTC"
        elif "qbx" in pool_id_lower or "qbitx" in pool_id_lower or "q-bitx" in pool_id_lower:
            primary = "QBX"

    # Method 2: Load from config file
    load_multi_coin_config()

    # Check which coins are enabled in MULTI_COIN_NODES
    # Also look for explicit primary designation
    explicit_primary = None
    for symbol, node in MULTI_COIN_NODES.items():
        if node.get('enabled', False):
            enabled.append(symbol)
            # Check if this coin is explicitly marked as primary
            if node.get('primary', False):
                explicit_primary = symbol

    # Determine if we're in multi-coin mode (more than 1 coin enabled)
    if len(enabled) > 1:
        multi_coin_mode = True

    # Set primary coin priority: POOL_ID > explicit primary > alphabetically first enabled
    if primary is None:
        if explicit_primary:
            primary = explicit_primary
        elif enabled:
            # Fall back to alphabetically first enabled coin (no coin preference)
            primary = sorted(enabled)[0]

    # If nothing enabled, log error but don't default to any coin
    if not enabled:
        print("⚠️ WARNING: No coins detected in config. Check pool configuration.")
        enabled = []
        primary = None

    # For solo mode, ensure only the primary coin is in enabled list
    if not multi_coin_mode and primary:
        enabled = [primary]

    active_coins["enabled"] = enabled
    active_coins["primary"] = primary
    active_coins["multi_coin_mode"] = multi_coin_mode
    active_coins["last_update"] = time.time()

    return active_coins


def get_primary_coin():
    """Get the primary (first enabled) coin symbol"""
    coins = get_enabled_coins()
    return coins["primary"]


def load_multi_coin_config():
    """Load RPC credentials for all configured coins from pool config.yaml"""
    global MULTI_COIN_NODES, _multi_coin_config_loaded
    _multi_coin_config_loaded = True  # Set before loading so concurrent calls don't pile up

    try:
        import yaml

        # Try V2 multi-coin config first
        if os.path.exists(POOL_CONFIG_PATH):
            with open(POOL_CONFIG_PATH, 'r') as f:
                config = yaml.safe_load(f)

                # Check for V2 multi-coin config
                coins = config.get('coins', [])
                if coins:
                    for coin_cfg in coins:
                        symbol = coin_cfg.get('symbol', '').upper()
                        if symbol in MULTI_COIN_NODES:
                            # V2 config: RPC credentials are in the 'nodes' array (first/primary node)
                            nodes = coin_cfg.get('nodes', [])
                            if nodes:
                                # Use first node (priority 0) as primary
                                primary_node = nodes[0]
                                MULTI_COIN_NODES[symbol]['rpc_host'] = primary_node.get('host', '127.0.0.1')
                                MULTI_COIN_NODES[symbol]['rpc_port'] = primary_node.get('port', MULTI_COIN_NODES[symbol]['rpc_port'])
                                MULTI_COIN_NODES[symbol]['rpc_user'] = primary_node.get('user', '')
                                MULTI_COIN_NODES[symbol]['rpc_password'] = primary_node.get('password', '')
                            else:
                                # Fallback to legacy 'daemon' format
                                daemon = coin_cfg.get('daemon', {})
                                MULTI_COIN_NODES[symbol]['rpc_host'] = daemon.get('host', '127.0.0.1')
                                MULTI_COIN_NODES[symbol]['rpc_port'] = daemon.get('port', MULTI_COIN_NODES[symbol]['rpc_port'])
                                MULTI_COIN_NODES[symbol]['rpc_user'] = daemon.get('user', '')
                                MULTI_COIN_NODES[symbol]['rpc_password'] = daemon.get('password', '')
                            MULTI_COIN_NODES[symbol]['enabled'] = coin_cfg.get('enabled', False)

                    # If any coin still has no RPC creds, inherit from top-level daemon config
                    top_daemon = config.get('daemon', {})
                    if top_daemon:
                        for coin_cfg in coins:
                            symbol = coin_cfg.get('symbol', '').upper()
                            if symbol in MULTI_COIN_NODES:
                                node = MULTI_COIN_NODES[symbol]
                                if not node.get('rpc_user') or not node.get('rpc_password'):
                                    node['rpc_host'] = top_daemon.get('host', node.get('rpc_host', '127.0.0.1'))
                                    node['rpc_port'] = top_daemon.get('port', node.get('rpc_port', 8332))
                                    node['rpc_user'] = top_daemon.get('user', '')
                                    node['rpc_password'] = top_daemon.get('password', '')

                    print(f"Loaded multi-coin config from {POOL_CONFIG_PATH}")
                    return True

                # Fallback to V1 single-coin config - detect coin from pool_id or port
                daemon = config.get('daemon', {})
                if daemon:
                    # Infer coin type from pool_id or daemon port
                    pool_id = config.get('pool', {}).get('id', '') or _POOL_ID_ENV
                    daemon_port = daemon.get('port', 0)

                    # Determine coin based on pool_id prefix or port
                    # Detection order matters: BCH/BC2 must be checked before BTC
                    # since 'bitcoincash'/'bitcoinii' contain 'bitcoin'
                    # DGB-SCRYPT must be checked before DGB
                    pool_id_lower = pool_id.lower()
                    detected_coin = None
                    default_port = 0

                    # SHA-256d coins
                    if 'bitcoincash' in pool_id_lower or 'bitcoin-cash' in pool_id_lower or 'bch' in pool_id_lower or daemon_port == 8432:
                        detected_coin = 'BCH'
                        default_port = 8432
                    elif 'bitcoinii' in pool_id_lower or 'bitcoin-ii' in pool_id_lower or 'bitcoin2' in pool_id_lower or 'bc2' in pool_id_lower or 'bcii' in pool_id_lower or daemon_port == 8339:
                        detected_coin = 'BC2'
                        default_port = 8339
                    elif 'bitcoin' in pool_id_lower or 'btc' in pool_id_lower or daemon_port == 8332:
                        detected_coin = 'BTC'
                        default_port = 8332
                    # Scrypt coins (check DGB-SCRYPT before DGB)
                    elif 'dgb-scrypt' in pool_id_lower or 'dgb_scrypt' in pool_id_lower or 'digibyte-scrypt' in pool_id_lower:
                        detected_coin = 'DGB-SCRYPT'
                        default_port = 14022  # Same port as DGB
                    elif 'digibyte' in pool_id_lower or 'dgb' in pool_id_lower or daemon_port == 14022:
                        detected_coin = 'DGB'
                        default_port = 14022
                    elif 'litecoin' in pool_id_lower or 'ltc' in pool_id_lower or daemon_port == 9332:
                        detected_coin = 'LTC'
                        default_port = 9332
                    elif 'dogecoin' in pool_id_lower or 'doge' in pool_id_lower or daemon_port == 22555:
                        detected_coin = 'DOGE'
                        default_port = 22555
                    elif 'namecoin' in pool_id_lower or 'nmc' in pool_id_lower or daemon_port == 8336:
                        detected_coin = 'NMC'
                        default_port = 8336
                    elif 'myriad' in pool_id_lower or 'xmy' in pool_id_lower or daemon_port == 10889:
                        detected_coin = 'XMY'
                        default_port = 10889
                    elif 'fractal' in pool_id_lower or 'fbtc' in pool_id_lower or daemon_port == 8340:
                        detected_coin = 'FBTC'
                        default_port = 8340
                    elif 'qbitx' in pool_id_lower or 'q-bitx' in pool_id_lower or 'qbx' in pool_id_lower or daemon_port == 8344:
                        detected_coin = 'QBX'
                        default_port = 8344
                    elif 'pepecoin' in pool_id_lower or 'pep' in pool_id_lower or daemon_port == 33873:
                        detected_coin = 'PEP'
                        default_port = 33873
                    elif 'catcoin' in pool_id_lower or 'cat' in pool_id_lower or daemon_port == 9932:
                        detected_coin = 'CAT'
                        default_port = 9932
                    if not detected_coin:
                        # Cannot determine coin - require explicit config
                        print(f"⚠️ WARNING: V1 config detected but cannot determine coin type. Use V2 config format.")
                        return False

                    MULTI_COIN_NODES[detected_coin]['rpc_host'] = daemon.get('host', '127.0.0.1')
                    MULTI_COIN_NODES[detected_coin]['rpc_port'] = daemon.get('port', default_port)
                    MULTI_COIN_NODES[detected_coin]['rpc_user'] = daemon.get('user', '')
                    MULTI_COIN_NODES[detected_coin]['rpc_password'] = daemon.get('password', '')
                    MULTI_COIN_NODES[detected_coin]['enabled'] = True
                    print(f"Loaded single-coin ({detected_coin}) config from {POOL_CONFIG_PATH}")
                    return True

    except Exception as e:
        print(f"Could not load multi-coin config: {e}")

    return False


def get_sha256_difficulty(symbol):
    """Get network difficulty for a coin's mining algorithm.

    Handles DigiByte's multi-algorithm response format where difficulty
    is returned as {"difficulties": {"sha256d": N, "scrypt": N, ...}}
    or the older format {"sha256d": {"difficulty": N, ...}}.

    For DGB-SCRYPT, extracts the scrypt difficulty instead of sha256d.
    For single-algo coins (BTC, BCH, LTC), uses getdifficulty directly.
    """
    symbol = symbol.upper()

    # For DigiByte (multi-algo), extract the correct algorithm's difficulty
    if symbol in ("DGB", "DGB-SCRYPT"):
        # Determine which algo difficulty to extract
        if symbol == "DGB-SCRYPT":
            target_keys = ["scrypt", "SCRYPT", "Scrypt"]
        else:
            target_keys = ["sha256d", "sha-256d", "SHA256D"]
        result = coin_rpc(symbol, "getdifficulty")
        if result:
            # v8.22+ format: {"difficulties": {"sha256d": N, ...}}
            if isinstance(result, dict):
                difficulties = result.get("difficulties", {})
                if difficulties:
                    for key in target_keys:
                        if key in difficulties:
                            return float(difficulties[key])
                # Older format: keys directly in result
                for key in target_keys:
                    if key in result:
                        val = result[key]
                        # Could be a number or nested object
                        if isinstance(val, (int, float)):
                            return float(val)
                        elif isinstance(val, dict):
                            return float(val.get("difficulty", 0))
            elif isinstance(result, (int, float)):
                # Single float value (unlikely for DGB but handle it)
                return float(result)
    else:
        # Single-algo coins (BTC, BCH) - getdifficulty returns a float
        result = coin_rpc(symbol, "getdifficulty")
        if result is not None:
            if isinstance(result, (int, float)):
                return float(result)
            # Fallback to getmininginfo
            mining = coin_rpc(symbol, "getmininginfo")
            if mining:
                return float(mining.get("difficulty", 0))

    # Last resort: getmininginfo difficulty field
    mining = coin_rpc(symbol, "getmininginfo")
    if mining:
        diff = mining.get("difficulty", 0)
        if isinstance(diff, dict):
            # Multi-algo format in getmininginfo
            algo_keys = (["scrypt", "SCRYPT", "Scrypt"] if symbol == "DGB-SCRYPT"
                         else ["sha256d", "sha-256d", "SHA256D"])
            for key in algo_keys:
                if key in diff:
                    return float(diff[key])
        elif isinstance(diff, (int, float)):
            return float(diff)

    return 0


def _get_cached_coin_version(symbol):
    """Read version from coin-versions cache (fallback for daemons with broken --version/subversion)."""
    try:
        ver_file = Path(os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")) / "config" / "coin-versions" / f"{symbol.upper()}.ver"
        if ver_file.is_file():
            return ver_file.read_text().strip()
    except OSError:
        pass
    return ""


def coin_rpc(symbol, method, params=None):
    """Make an RPC call to a specific coin's node"""
    symbol = symbol.upper()
    if symbol not in MULTI_COIN_NODES:
        return None

    node = MULTI_COIN_NODES[symbol]
    if not node['rpc_user'] or not node['rpc_password']:
        load_multi_coin_config()
        node = MULTI_COIN_NODES[symbol]

    # If credentials still missing, try loading from the coin's daemon conf file
    if not node['rpc_user'] or not node['rpc_password']:
        conf_path = node.get('config_file', '')
        if conf_path and os.path.exists(conf_path):
            try:
                with open(conf_path, 'r') as f:
                    for line in f:
                        line = line.strip()
                        if line.startswith('rpcuser='):
                            node['rpc_user'] = line.split('=', 1)[1].strip().strip('"').strip("'")
                        elif line.startswith('rpcpassword='):
                            node['rpc_password'] = line.split('=', 1)[1].strip().strip('"').strip("'")
                if node['rpc_user'] and node['rpc_password']:
                    print(f"Loaded RPC credentials from {conf_path} for {symbol}")
            except Exception as e:
                print(f"Could not read {conf_path}: {e}")

    if not node['rpc_user'] or not node['rpc_password']:
        return None

    try:
        payload = {
            "jsonrpc": "1.0",
            "id": f"dashboard-{symbol}",
            "method": method,
            "params": params or []
        }
        response = requests.post(
            f"http://{node['rpc_host']}:{node['rpc_port']}",
            json=payload,
            auth=(node['rpc_user'], node['rpc_password']),
            timeout=10
        )
        if response.status_code == 401:
            print(f"RPC auth failed ({symbol} {method}): 401 Unauthorized")
            return None
        result = response.json()
        if result.get("error"):
            print(f"RPC error ({symbol} {method}): {result['error']}")
            return None
        return result.get("result")
    except Exception as e:
        print(f"RPC error ({symbol} {method}): {e}")
        return None


def fetch_coin_node_health(symbol):
    """Fetch health status for a specific coin's node"""
    symbol = symbol.upper()
    if symbol not in MULTI_COIN_NODES:
        return {"status": "unknown", "error": f"Unknown coin: {symbol}"}

    node = MULTI_COIN_NODES[symbol]

    health = {
        "symbol": symbol,
        "name": node['name'],
        "status": "offline",
        "status_message": "",
        "enabled": node['enabled'],
        "version": "",
        "chain": "",
        "blocks": 0,
        "headers": 0,
        "sync_progress": 0,
        "connections": 0,
        "difficulty": 0,
        "network_hashrate": 0,
        "mempool_size": 0,
        "size_on_disk_gb": 0,
        "uptime": 0,
        "stratum_ports": node['stratum_ports'],
        "data_dir": node['data_dir']
    }

    if not node['enabled']:
        health["status"] = "disabled"
        health["status_message"] = "Node is not enabled in configuration"
        return health

    if not node['rpc_user'] or not node['rpc_password']:
        load_multi_coin_config()
        node = MULTI_COIN_NODES[symbol]

    if not node['rpc_user'] or not node['rpc_password']:
        health["status"] = "unconfigured"
        health["status_message"] = f"RPC credentials not found for {symbol}"
        return health

    try:
        # Get blockchain info
        bc_info = coin_rpc(symbol, "getblockchaininfo")
        if bc_info:
            health["status"] = "online"
            health["chain"] = bc_info.get("chain", "")
            health["blocks"] = bc_info.get("blocks", 0)
            health["headers"] = bc_info.get("headers", 0)
            health["sync_progress"] = round(bc_info.get("verificationprogress", 0) * 100, 2)
            health["size_on_disk_gb"] = round(bc_info.get("size_on_disk", 0) / 1024 / 1024 / 1024, 2)
            # For multi-algo coins (DGB), getblockchaininfo "difficulty" is
            # whichever algo mined the last block - NOT the SHA256 difficulty.
            # Use get_sha256_difficulty() which queries getdifficulty for the
            # correct per-algo value.
            sha256_diff = get_sha256_difficulty(symbol)
            health["difficulty"] = sha256_diff if sha256_diff > 0 else bc_info.get("difficulty", 0)

        # Get network info
        net_info = coin_rpc(symbol, "getnetworkinfo")
        if net_info:
            health["version"] = net_info.get("subversion", "")
            health["connections"] = net_info.get("connections", 0)

        # Override with version cache if available — some daemons (e.g. QBX)
        # report incorrect version in their subversion string
        cached_ver = _get_cached_coin_version(symbol)
        if cached_ver:
            health["version"] = cached_ver

        # Get mempool info
        mempool_info = coin_rpc(symbol, "getmempoolinfo")
        if mempool_info:
            health["mempool_size"] = mempool_info.get("size", 0)

        # Get uptime
        uptime = coin_rpc(symbol, "uptime")
        if uptime:
            health["uptime"] = uptime

        # Calculate network hashrate — prefer node RPC (actual recent block timing)
        # over the formula (diff * 2^32 / block_time) which assumes target block rate
        rpc_nhps = pool_stats_cache.get("node_networkhashps", 0)
        if rpc_nhps and rpc_nhps > 0:
            health["network_hashrate"] = rpc_nhps
        elif health["difficulty"] > 0:
            coin_block_times = {
                "DGB": 15, "BTC": 600, "BCH": 600, "BC2": 600,
                "LTC": 150, "DOGE": 60, "DGB-SCRYPT": 15,
                "PEP": 60, "CAT": 600,
                "NMC": 600, "SYS": 60, "XMY": 60, "FBTC": 30, "QBX": 150
            }
            block_time = coin_block_times.get(symbol, 600)
            health["network_hashrate"] = health["difficulty"] * (2**32) / block_time

    except Exception as e:
        health["status"] = "error"
        health["status_message"] = str(e)

    return health


def fetch_all_nodes_health():
    """Fetch health status for all configured coin nodes"""
    global multi_coin_health_cache

    # Rate limit to every 10 seconds
    if time.time() - multi_coin_health_cache["last_update"] < 10:
        return multi_coin_health_cache["nodes"]

    # Load config if not loaded
    if not any(node['rpc_user'] for node in MULTI_COIN_NODES.values()):
        load_multi_coin_config()

    nodes = {}
    for symbol in MULTI_COIN_NODES:
        nodes[symbol] = fetch_coin_node_health(symbol)

    multi_coin_health_cache["nodes"] = nodes
    multi_coin_health_cache["last_update"] = time.time()

    return nodes


def fetch_health_data():
    """Fetch health status for pool and node"""
    global health_cache

    # Rate limit to every 10 seconds
    if time.time() - health_cache["last_update"] < 10:
        return health_cache

    # === POOL HEALTH ===
    pool_health = {
        "status": "offline",
        "uptime": 0,
        "connections": 0,
        "hashrate": 0,
        "shares_per_sec": 0,
        "accepted_shares": 0,
        "rejected_shares": 0,
        "stale_shares": 0,
        "invalid_shares": 0,
        "blocks_found": 0,
        "last_block_time": None,
        "jobs_dispatched": 0,
        "zmq_status": "unknown",
        "zmq_messages": 0,
        "zmq_reconnects": 0,
        "block_notify_mode": "unknown",
        "memory_mb": 0,
        "goroutines": 0,
        "avg_share_diff": 0,
        "best_share_diff": 0,
        "reject_rate": 0
    }

    try:
        # Get pool stats from API
        response = requests.get(f"{POOL_API_URL}/api/pools", timeout=5)
        if response.status_code == 200:
            data = response.json()
            if data.get("pools") and len(data["pools"]) > 0:
                pool = data["pools"][0]
                stats = pool.get("poolStats", {})
                pool_health["status"] = "online"
                pool_health["connections"] = stats.get("connectedMiners", 0)
                pool_health["hashrate"] = stats.get("poolHashrate", 0)
                pool_health["shares_per_sec"] = stats.get("sharesPerSecond", 0)

        # Get Prometheus metrics for detailed stats
        metrics = fetch_prometheus_metrics()
        # Note: Empty dict {} is truthy, so check for actual content
        if metrics and len(metrics) > 0:
            accepted = int(metrics.get("stratum_shares_accepted_total", 0))
            # stratum_shares_rejected_total is a labeled CounterVec with {reason="..."}
            # parse_prometheus_metrics() now sums all label variants into the bare name
            rejected = int(metrics.get("stratum_shares_rejected_total", 0))
            stale = int(metrics.get("stratum_shares_stale_total", 0))
            # Invalid shares are a subset of rejected shares (reason="invalid_*")
            # Sum all rejected variants whose reason starts with "invalid"
            invalid = int(sum(
                v for k, v in metrics.items()
                if k.startswith('stratum_shares_rejected_total{') and 'invalid' in k
            ))

            pool_health["accepted_shares"] = accepted
            pool_health["rejected_shares"] = rejected
            pool_health["stale_shares"] = stale
            pool_health["invalid_shares"] = invalid
            pool_health["blocks_found"] = int(metrics.get("stratum_blocks_found_total", 0))
            pool_health["jobs_dispatched"] = int(metrics.get("stratum_jobs_dispatched_total", 0))
            pool_health["goroutines"] = int(metrics.get("go_goroutines", 0))
            pool_health["memory_mb"] = round(metrics.get("go_memstats_alloc_bytes", 0) / 1024 / 1024, 1)
            pool_health["best_share_diff"] = metrics.get("stratum_best_share_difficulty", 0)
            pool_health["avg_share_diff"] = metrics.get("stratum_avg_share_difficulty", 0)

            # Calculate reject rate
            total_shares = accepted + rejected
            if total_shares > 0:
                pool_health["reject_rate"] = round((rejected / total_shares) * 100, 2)

            # Process uptime from start time
            start_time = metrics.get("process_start_time_seconds", 0)
            if start_time > 0:
                pool_health["uptime"] = int(time.time() - start_time)

            # ZMQ status from Prometheus metrics
            zmq_connected = metrics.get("stratum_zmq_connected", 0)
            block_notify_mode = metrics.get("stratum_block_notify_mode", 0)
            zmq_messages = int(metrics.get("stratum_zmq_messages_received_total", 0))
            zmq_reconnects = int(metrics.get("stratum_zmq_reconnects_total", 0))

            if zmq_connected >= 1:
                pool_health["zmq_status"] = "connected"
            else:
                pool_health["zmq_status"] = "disconnected"

            if block_notify_mode >= 1:
                pool_health["block_notify_mode"] = "zmq"
            else:
                pool_health["block_notify_mode"] = "polling"

            pool_health["zmq_messages"] = zmq_messages
            pool_health["zmq_reconnects"] = zmq_reconnects

    except Exception as e:
        print(f"Error fetching pool health: {e}")

    # === NODE HEALTH ===
    node_health = {
        "status": "offline",
        "status_message": "",
        "version": "",
        "chain": "",
        "blocks": 0,
        "headers": 0,
        "sync_progress": 0,
        "connections": 0,
        "connections_in": 0,
        "connections_out": 0,
        "difficulty": 0,
        "network_hashrate": 0,
        "mempool_size": 0,
        "mempool_bytes": 0,
        "uptime": 0,
        "size_on_disk_gb": 0,
        "verification_progress": 0,
        "bytes_sent": 0,
        "bytes_recv": 0,
        "relay_fee": 0,
        "warnings": ""
    }

    # Detect primary coin for RPC calls
    coins = get_enabled_coins()
    primary_coin = coins.get("primary")

    # If no coin detected, return unconfigured status
    if not primary_coin:
        node_health["status"] = "unconfigured"
        node_health["status_message"] = "No coin configured. Check pool configuration."
        health_cache["pool"] = pool_health
        health_cache["node"] = node_health
        health_cache["last_update"] = time.time()
        return health_cache

    # Check if we have RPC credentials for the primary coin
    primary_node = MULTI_COIN_NODES.get(primary_coin)
    if not primary_node or not primary_node.get("rpc_user") or not primary_node.get("rpc_password"):
        load_pool_config()
        primary_node = MULTI_COIN_NODES.get(primary_coin)

    if not primary_node or not primary_node.get("rpc_user") or not primary_node.get("rpc_password"):
        node_health["status"] = "unconfigured"
        node_health["status_message"] = f"RPC credentials not found for {primary_coin}. Check /spiralpool/config/config.yaml"
        health_cache["pool"] = pool_health
        health_cache["node"] = node_health
        health_cache["last_update"] = time.time()
        return health_cache

    try:
        # Get blockchain info using coin-aware RPC
        bc_info = coin_rpc(primary_coin, "getblockchaininfo")
        if bc_info:
            node_health["status"] = "online"
            node_health["chain"] = bc_info.get("chain", "")
            node_health["blocks"] = bc_info.get("blocks", 0)
            node_health["headers"] = bc_info.get("headers", 0)
            node_health["verification_progress"] = bc_info.get("verificationprogress", 0)
            # QBX has near-zero verificationprogress due to low chain work — use blocks/headers instead
            _blocks = bc_info.get("blocks", 0)
            _headers = bc_info.get("headers", 0)
            if primary_coin and primary_coin.upper() in ("QBX", "QBITX") and _headers > 0:
                node_health["sync_progress"] = round(min(_blocks / _headers * 100, 100), 2)
            else:
                node_health["sync_progress"] = round(bc_info.get("verificationprogress", 0) * 100, 2)
            node_health["size_on_disk_gb"] = round(bc_info.get("size_on_disk", 0) / 1024 / 1024 / 1024, 2)
            # For multi-algo coins (DGB), getblockchaininfo "difficulty" is
            # whichever algo mined the last block - NOT the SHA256 difficulty.
            sha256_diff = get_sha256_difficulty(primary_coin)
            node_health["difficulty"] = sha256_diff if sha256_diff > 0 else bc_info.get("difficulty", 0)

        # Get network info
        net_info = coin_rpc(primary_coin, "getnetworkinfo")
        if net_info:
            node_health["version"] = net_info.get("subversion", "").strip("/")
            node_health["connections"] = net_info.get("connections", 0)
            node_health["connections_in"] = net_info.get("connections_in", 0)
            node_health["connections_out"] = net_info.get("connections_out", 0)
            node_health["relay_fee"] = net_info.get("relayfee", 0)
            node_health["warnings"] = net_info.get("warnings", "")

        # Override with version cache if available — some daemons (e.g. QBX)
        # report incorrect version in their subversion string
        cached_ver = _get_cached_coin_version(primary_coin)
        if cached_ver:
            node_health["version"] = cached_ver

        # Get mempool info
        mempool = coin_rpc(primary_coin, "getmempoolinfo")
        if mempool:
            node_health["mempool_size"] = mempool.get("size", 0)
            node_health["mempool_bytes"] = mempool.get("bytes", 0)

        # Get mining info for network hashrate
        # Prefer cached node RPC getnetworkhashps (actual recent block timing)
        # over the formula which assumes blocks arrive at target rate
        mining = coin_rpc(primary_coin, "getmininginfo")
        if not node_health.get("network_hashrate"):
            cached_nhps = pool_stats_cache.get("node_networkhashps", 0)
            if cached_nhps and cached_nhps > 0:
                node_health["network_hashrate"] = cached_nhps
            elif mining:
                diff_val = node_health.get("difficulty", 0) or float(mining.get("difficulty", 0))
                if diff_val > 0:
                    coin_block_times = {
                        "DGB": 15, "BTC": 600, "BCH": 600, "BC2": 600,
                        "LTC": 150, "DOGE": 60, "DGB-SCRYPT": 15,
                        "PEP": 60, "CAT": 600,
                        "NMC": 600, "SYS": 60, "XMY": 60, "FBTC": 30, "QBX": 150
                    }
                    bt = coin_block_times.get(primary_coin, 600)
                    node_health["network_hashrate"] = diff_val * (2**32) / bt
                else:
                    node_health["network_hashrate"] = mining.get("networkhashps", 0)

        # Get network traffic stats via getnettotals
        net_totals = coin_rpc(primary_coin, "getnettotals")
        if net_totals:
            node_health["bytes_sent"] = net_totals.get("totalbytessent", 0)
            node_health["bytes_recv"] = net_totals.get("totalbytesrecv", 0)

        # Get uptime
        uptime = coin_rpc(primary_coin, "uptime")
        if uptime:
            node_health["uptime"] = uptime

    except Exception as e:
        print(f"Error fetching node health for {primary_coin}: {e}")

    health_cache["pool"] = pool_health
    health_cache["node"] = node_health
    health_cache["last_update"] = time.time()

    # Always populate nodes dict for all enabled coins (solo, multi, merge)
    enabled_coins = coins.get("enabled", [])
    all_nodes_health = {}
    for coin_symbol in enabled_coins:
        if coin_symbol == primary_coin:
            all_nodes_health[coin_symbol] = node_health.copy()
            all_nodes_health[coin_symbol]["coin"] = coin_symbol
            all_nodes_health[coin_symbol]["name"] = MULTI_COIN_NODES.get(coin_symbol, {}).get("name", coin_symbol)
            all_nodes_health[coin_symbol]["algorithm"] = MULTI_COIN_NODES.get(coin_symbol, {}).get("algorithm", "")
            mm = MULTI_COIN_NODES.get(coin_symbol, {}).get("merge_mining")
            all_nodes_health[coin_symbol]["merge_mining"] = mm
        else:
            coin_health = fetch_coin_node_health(coin_symbol)
            coin_health["algorithm"] = MULTI_COIN_NODES.get(coin_symbol, {}).get("algorithm", "")
            mm = MULTI_COIN_NODES.get(coin_symbol, {}).get("merge_mining")
            coin_health["merge_mining"] = mm
            all_nodes_health[coin_symbol] = coin_health
    health_cache["nodes"] = all_nodes_health
    health_cache["multi_coin_mode"] = len(enabled_coins) > 1

    return health_cache


def _compute_network_hashrate(difficulty):
    """Compute network hashrate (H/s).

    Prefers the node's getnetworkhashps RPC value (cached in pool_stats_cache)
    which uses a moving average over recent blocks and reflects actual network
    performance.  Falls back to the formula  difficulty * 2^32 / block_time
    when the RPC value is unavailable.
    """
    # Prefer node RPC networkhashps — it uses actual recent block timing
    rpc_nhps = pool_stats_cache.get("node_networkhashps", 0)
    if rpc_nhps and rpc_nhps > 0:
        return rpc_nhps

    if not difficulty or difficulty <= 0:
        return 0
    bt = block_reward_cache.get("block_time", 0) or 600
    return difficulty * (2**32) / bt


def record_historical_data():
    """Record current stats to historical data (called periodically)"""
    timestamp = time.time()

    # Pool hashrate
    pool_hashrate = pool_stats_cache.get("pool_hashrate", 0)
    historical_data["pool_hashrate"].append({
        "time": timestamp,
        "value": pool_hashrate
    })

    # Connected miners from pool
    connected = pool_stats_cache.get("connected_miners", 0)
    historical_data["connected_miners"].append({
        "time": timestamp,
        "value": connected
    })

    # Shares per second
    sps = pool_stats_cache.get("shares_per_second", 0)
    historical_data["shares_per_second"].append({
        "time": timestamp,
        "value": sps
    })

    # Miner totals
    totals = miner_cache.get("totals", {})
    historical_data["miner_hashrate"].append({
        "time": timestamp,
        "value": totals.get("hashrate_ths", 0)
    })
    historical_data["power_watts"].append({
        "time": timestamp,
        "value": totals.get("power_watts", 0)
    })

    # Network difficulty and hashrate (for Statistics chart grid)
    net_diff = pool_stats_cache.get("network_difficulty", 0)
    historical_data["network_difficulty"].append({
        "time": timestamp,
        "value": net_diff
    })
    # Use RPC getnetworkhashps (accurate, uses actual block timing) when available,
    # fall back to formula (inaccurate when blocks arrive faster/slower than target)
    net_hashrate = _compute_network_hashrate(net_diff)
    historical_data["network_hashrate"].append({
        "time": timestamp,
        "value": net_hashrate
    })

    # Per-miner hashrate tracking (local — no stratum API dependency)
    miners = miner_cache.get("miners", {})
    for name, data in miners.items():
        if name not in historical_data["per_miner_hashrate"]:
            historical_data["per_miner_hashrate"][name] = deque(maxlen=HISTORY_MAX_POINTS)
        hashrate_ghs = data.get("hashrate_ghs", 0) or 0
        hashrate_ths = hashrate_ghs / 1000 if hashrate_ghs else 0
        historical_data["per_miner_hashrate"][name].append({
            "time": timestamp,
            "value": hashrate_ths
        })


# Track miners.json mtime for auto-import from spiralpool-scan
_last_sentinel_db_mtime = 0

def _get_sentinel_db_path():
    """Get the path to the Sentinel miners.json database."""
    install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
    for path in [Path(install_dir) / "data" / "miners.json", Path("/spiralpool/data/miners.json")]:
        if path.exists():
            return path
    return None

def _check_sentinel_db_changed():
    """Check if miners.json was modified externally (e.g., by spiralpool-scan) and auto-import."""
    global _last_sentinel_db_mtime
    try:
        db_path = _get_sentinel_db_path()
        if not db_path:
            return
        current_mtime = db_path.stat().st_mtime
        if _last_sentinel_db_mtime == 0:
            _last_sentinel_db_mtime = current_mtime
            return
        if current_mtime > _last_sentinel_db_mtime:
            _last_sentinel_db_mtime = current_mtime
            print("[BG] miners.json changed externally, auto-importing...")
            imported, skipped, errors = import_miners_from_sentinel()
            if imported > 0:
                print(f"[BG] Auto-imported {imported} miners from miners.json")
            if errors:
                print(f"[BG] Auto-import warnings: {errors}")
    except Exception as e:
        print(f"[BG] Sentinel DB check error: {e}")


def background_data_collection():
    """Background thread for collecting historical data, alerts, watchdog, and real-time updates.

    Each operation is individually try/excepted so one failure (e.g., pool API timeout)
    doesn't skip the entire cycle's remaining operations.
    """
    last_audit_save = time.time()
    last_history_save = time.time()

    # Give gunicorn worker time to accept first requests before starting
    # heavy network I/O that competes for the GIL (fixes first-request timeout)
    time.sleep(5)

    while True:
        try:
            # HA BACKUP NODE OPTIMIZATION: Keep role cache fresh, skip polling on BACKUP
            fetch_ha_status()
            if ha_status_cache.get("enabled") and ha_status_cache.get("local_role") not in ("MASTER", "STANDALONE", "UNKNOWN"):
                time.sleep(60)
                continue
        except Exception as e:
            print(f"[BG] HA status fetch error: {e}")

        try:
            fetch_pool_stats()
        except Exception as e:
            print(f"[BG] Pool stats fetch error: {e}")

        # Cache the node's getnetworkhashps — uses actual recent block timing
        # for more accurate network hashrate than the formula (diff * 2^32 / bt)
        try:
            coins = get_enabled_coins()
            primary_coin = coins.get("primary")
            if primary_coin:
                nhps = coin_rpc(primary_coin, "getnetworkhashps")
                if nhps and isinstance(nhps, (int, float)) and nhps > 0:
                    pool_stats_cache["node_networkhashps"] = float(nhps)
        except Exception as e:
            print(f"[BG] Network hashrate RPC error: {e}")

        # Refresh miner device cache in background so the /api/miners request handler
        # rarely needs to do a slow synchronous fetch (which can block gunicorn workers).
        try:
            fetch_all_miners()
        except Exception as e:
            print(f"[BG] Miner fetch error: {e}")

        try:
            fetch_prometheus_metrics()
        except Exception as e:
            print(f"[BG] Prometheus fetch error: {e}")

        try:
            record_historical_data()
        except Exception as e:
            print(f"[BG] Historical data recording error: {e}")

        try:
            check_enhanced_alerts()
        except Exception as e:
            print(f"[BG] Alert check error: {e}")

        # NOTE: Hashrate watchdog/auto-restart is handled by Spiral Sentinel

        try:
            update_rejection_analysis()
        except Exception as e:
            print(f"[BG] Rejection analysis error: {e}")

        try:
            update_etb_calculation()
        except Exception as e:
            print(f"[BG] ETB calculation error: {e}")

        try:
            update_luck_tracker()
        except Exception as e:
            print(f"[BG] Luck tracker error: {e}")

        try:
            update_difficulty_predictor()
        except Exception as e:
            print(f"[BG] Difficulty predictor error: {e}")

        try:
            update_session_stats()
        except Exception as e:
            print(f"[BG] Session stats error: {e}")

        try:
            update_network_health()
        except Exception as e:
            print(f"[BG] Network health error: {e}")

        try:
            _check_sentinel_db_changed()
        except Exception as e:
            print(f"[BG] Sentinel DB watch error: {e}")

        try:
            broadcast_realtime_update()
        except Exception as e:
            print(f"[BG] WebSocket broadcast error: {e}")

        try:
            if time.time() - last_audit_save > 300:
                save_share_audit_log()
                last_audit_save = time.time()
        except Exception as e:
            print(f"[BG] Share audit save error: {e}")

        try:
            if time.time() - last_history_save > 300:
                save_historical_data()
                save_activity_feed()
                last_history_save = time.time()
        except Exception as e:
            print(f"[BG] Historical data save error: {e}")

        time.sleep(60)  # Collect every minute


def ensure_config_dir():
    """Ensure configuration directory exists"""
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)


def load_config():
    """Load configuration from file"""
    ensure_config_dir()
    if CONFIG_FILE.exists():
        try:
            with open(CONFIG_FILE, 'r') as f:
                config = json.load(f)
                # Merge with defaults for any missing keys
                for key, value in DEFAULT_CONFIG.items():
                    if key not in config:
                        config[key] = value

                # CRITICAL: Also merge missing device types into config["devices"]
                # This ensures old configs get new device types (hammer, goldshell, etc.)
                if "devices" in config and isinstance(config["devices"], dict):
                    for device_type, default_list in DEFAULT_CONFIG.get("devices", {}).items():
                        if device_type not in config["devices"]:
                            config["devices"][device_type] = default_list

                return config
        except Exception as e:
            print(f"Error loading config: {e}")
    # CRITICAL: Use deep copy to prevent mutation of DEFAULT_CONFIG
    # shallow copy() would share nested dicts (devices, power_cost) with the original
    import copy
    return copy.deepcopy(DEFAULT_CONFIG)


def _atomic_json_save(filepath, data, indent=None):
    """R-13 FIX: Atomic JSON file write using tempfile + rename.
    Prevents corruption if power fails mid-write. Pattern matches
    Sentinel's proven implementation (Spiral Sentinel — SpiralSentinel.py:11516-11540)."""
    target_dir = os.path.dirname(filepath) or "."
    fd, tmp_path = tempfile.mkstemp(suffix='.tmp', prefix='dash_', dir=target_dir)
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(data, f, indent=indent)
            f.flush()
            os.fsync(f.fileno())
        shutil.move(tmp_path, filepath)
    except Exception:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
        raise


def save_config(config):
    """Save configuration to file"""
    ensure_config_dir()
    _atomic_json_save(str(CONFIG_FILE), config, indent=2)


def import_miners_from_sentinel():
    """
    Import miners discovered by spiralpool-scan (stored in /spiralpool/data/miners.json)
    into the dashboard configuration.
    Returns tuple of (imported_count, skipped_count, errors)
    """
    import re

    # IP address validation regex (IPv4 only for miner devices)
    IP_PATTERN = re.compile(r'^(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$')

    # Nickname sanitization: allow alphanumeric, spaces, hyphens, underscores only
    def sanitize_nickname(name):
        if not name or not isinstance(name, str):
            return None
        # Remove any characters that aren't alphanumeric, space, hyphen, underscore, or period
        sanitized = re.sub(r'[^a-zA-Z0-9 ._-]', '', name)
        # Limit length to prevent abuse
        return sanitized[:64] if sanitized else None

    # Miner database location - check multiple possible paths
    # Priority: environment variable > relative to dashboard > common install locations
    install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
    print(f"[IMPORT] Starting import, SPIRALPOOL_INSTALL_DIR={install_dir}")

    possible_paths = [
        Path(install_dir) / "data" / "miners.json",           # Environment-specified location
        Path(__file__).parent.parent.parent / "data" / "miners.json",  # Relative to src/dashboard
        Path("/spiralpool/data/miners.json"),                 # Linux default
        Path("/var/lib/spiralpool/miners.json"),              # Linux FHS-compliant location
        # NOTE: Home dir path removed — ProtectHome=yes blocks access under systemd
    ]

    # On Windows, also check common locations
    if os.name == 'nt':
        possible_paths.insert(0, Path(os.environ.get("PROGRAMDATA", "C:/ProgramData")) / "SpiralPool" / "miners.json")
        possible_paths.insert(0, Path(os.environ.get("LOCALAPPDATA", "")) / "SpiralPool" / "miners.json")

    sentinel_db = None
    for path in possible_paths:
        try:
            exists = path.exists()
            print(f"[IMPORT] Checking path {path}: exists={exists}")
            if exists:
                sentinel_db = path
                print(f"[IMPORT] Found sentinel database at: {path}")
                break
        except (PermissionError, OSError) as e:
            print(f"[IMPORT] Permission error checking {path}: {e}")
            continue

    if not sentinel_db:
        checked_paths = ", ".join(str(p) for p in possible_paths[:6])
        print(f"[IMPORT] No miner database found. Checked: {checked_paths}")
        return 0, 0, [f"No miner database found. Run 'spiralpool-scan' to discover miners. Checked: {checked_paths}"]

    try:
        with open(sentinel_db, 'r') as f:
            sentinel_data = json.load(f)
        app.logger.info(f"[IMPORT] Loaded sentinel data, keys: {list(sentinel_data.keys())}")
    except (json.JSONDecodeError, OSError) as e:
        app.logger.error(f"[IMPORT] Failed to read sentinel database: {e}")
        return 0, 0, [f"Failed to read sentinel database: {e}"]

    miners = sentinel_data.get("miners", {})
    print(f"[IMPORT] Found {len(miners) if isinstance(miners, dict) else 'invalid'} miners in database")
    if not miners:
        # Check if the data structure is different (e.g., list instead of dict)
        if isinstance(sentinel_data, list):
            print(f"[IMPORT] Database is a list with {len(sentinel_data)} entries")
            return 0, 0, [f"Sentinel database has list format ({len(sentinel_data)} entries) - expected dict with 'miners' key"]
        print(f"[IMPORT] Raw sentinel_data type: {type(sentinel_data)}, content preview: {str(sentinel_data)[:200]}")
        return 0, 0, ["Sentinel database is empty"]

    # Validate miners is a dict
    if not isinstance(miners, dict):
        return 0, 0, ["Invalid miners format in sentinel database"]

    # Load current dashboard config
    config = load_config()
    if "devices" not in config:
        config["devices"] = {}

    imported = 0
    skipped = 0
    errors = []

    # Map sentinel types to dashboard types (whitelist approach)
    # Comprehensive mapping for ALL supported miner types
    type_mapping = {
        # AxeOS HTTP API devices (port 80)
        "axeos": "axeos",
        "bitaxe": "axeos",           # BitAxe family -> axeos
        "nmaxe": "nmaxe",
        "nerdaxe": "nerdqaxe",       # NerdAxe -> nerdqaxe (same API)
        "nerdqaxe": "nerdqaxe",
        "nerdoctaxe": "nerdqaxe",    # NerdOctaxe -> nerdqaxe (same API)
        "qaxe": "qaxe",
        "qaxeplus": "qaxeplus",
        "esp32miner": "esp32miner",    # ESP32 Miner (ESP32-based)
        "esp32": "esp32miner",         # ESP32 alias (from spiralpool-scan manual add)
        "hammer": "hammer",          # Scrypt
        "goldshell": "goldshell",    # Scrypt
        "luckyminer": "luckyminer",  # Lucky Miner LV06/LV07/LV08
        "jingleminer": "jingleminer", # Jingle Miner BTC Solo Pro/Lite
        "zyber": "zyber",            # Zyber 8G/8GP/8S
        # CGMiner API devices (port 4028)
        "avalon": "avalon",
        "antminer": "antminer",
        "antminer_scrypt": "antminer_scrypt",
        "whatsminer": "whatsminer",
        "innosilicon": "innosilicon",
        "futurebit": "futurebit",
        "canaan": "canaan",          # Canaan AvalonMiner
        "ebang": "ebang",            # Ebang Ebit
        "gekkoscience": "gekkoscience", # GekkoScience Compac F, NewPac, R606
        "ipollo": "ipollo",          # iPollo V1, V1 Mini, G1
        "epic": "epic",              # ePIC BlockMiner
        "elphapex": "elphapex",      # Elphapex DG1, DG Home
        # Custom firmware (REST API)
        "braiins": "braiins",        # BraiinsOS/BOS+
        "vnish": "vnish",            # Vnish firmware
        "luxos": "luxos",            # LuxOS firmware
    }

    for ip, miner_info in miners.items():
        try:
            # SECURITY: Validate IP address format
            if not isinstance(ip, str) or not IP_PATTERN.match(ip):
                errors.append(f"Invalid IP address format: {ip[:50] if isinstance(ip, str) else 'non-string'}")
                continue

            # Validate miner_info is a dict
            if not isinstance(miner_info, dict):
                errors.append(f"Invalid miner info for {ip}")
                continue

            sentinel_type = miner_info.get("type", "")
            if not isinstance(sentinel_type, str):
                sentinel_type = ""
            sentinel_type = sentinel_type.lower()

            # Use whitelist for device types, default to axeos
            device_type = type_mapping.get(sentinel_type, "axeos")

            # Initialize device type list if needed
            if device_type not in config["devices"]:
                config["devices"][device_type] = []

            # Check if IP already exists across ALL device types (prevent duplicates)
            ip_exists = False
            for dtype in config["devices"]:
                if isinstance(config["devices"][dtype], list):
                    for device in config["devices"][dtype]:
                        if isinstance(device, dict) and device.get("ip") == ip:
                            ip_exists = True
                            break
                if ip_exists:
                    break

            if ip_exists:
                print(f"[IMPORT] Skipping {ip} - already configured")
                skipped += 1
                continue

            print(f"[IMPORT] Importing {ip} as {device_type}")
            # SECURITY: Sanitize nickname
            raw_nickname = miner_info.get("nickname") or miner_info.get("name")
            nickname = sanitize_nickname(raw_nickname)
            if not nickname:
                # Generate safe default nickname from IP
                nickname = f"{device_type}_{ip.split('.')[-1]}"

            # Default watts by miner type (comprehensive list)
            default_watts_map = {
                # AxeOS HTTP API devices
                "nmaxe": 20,           # NMaxe: ~500 GH/s @ 20W
                "axeos": 80,           # Generic AxeOS device
                "nerdqaxe": 80,        # NerdQAxe++: ~1.6 TH/s @ 80W
                "qaxe": 80,            # QAxe: ~2 TH/s @ 80W
                "qaxeplus": 100,       # QAxe+: enhanced cooling
                "esp32miner": 2,        # ESP32 Miner: ESP32 only, ~1-2W
                "hammer": 25,          # Hammer Miner: Scrypt, 25W
                "goldshell": 2300,     # Goldshell: varies by model
                # CGMiner API devices
                "avalon": 140,         # Avalon Nano: ~140W
                "antminer": 3250,      # Antminer S19/S21: ~3250W
                "antminer_scrypt": 3250,  # Antminer L7/L9: ~3250W
                "whatsminer": 3400,    # Whatsminer M50/M60: ~3400W
                "innosilicon": 3500,   # Innosilicon A10/A11: ~3500W
                "futurebit": 200,      # FutureBit Apollo: ~200W
                "canaan": 3000,        # Canaan AvalonMiner: ~3000W
                "ebang": 2800,         # Ebang Ebit: ~2800W
                "braiins": 3250,       # BraiinsOS (S9/S17/S19/S21): varies, ~3250W avg
                "vnish": 3250,         # Vnish firmware (Antminer variants)
                "luxos": 3250,         # LuxOS firmware (Antminer variants)
                "luckyminer": 50,      # Lucky Miner LV06/LV07/LV08
                "jingleminer": 100,    # Jingle Miner BTC Solo Pro/Lite
                "zyber": 100,          # Zyber 8G/8GP/8S
                "gekkoscience": 5,     # GekkoScience USB miners
                "ipollo": 2000,        # iPollo V1/G1 series
                "epic": 3000,          # ePIC BlockMiner
                "elphapex": 3000,      # Elphapex DG1/DG Home
            }

            # Create device entry with validated/sanitized data
            # Use 'name' field for consistency with scan code and fleet stats
            new_device = {
                "ip": ip,
                "name": nickname,
                "watts": default_watts_map.get(device_type, 100)
            }

            # Preserve model info from sentinel database if available
            model_info = miner_info.get("model")
            if model_info and isinstance(model_info, str):
                # Sanitize model string (alphanumeric, spaces, hyphens, parentheses only)
                sanitized_model = re.sub(r'[^a-zA-Z0-9 ._()-]', '', model_info)[:64]
                if sanitized_model:
                    new_device["model"] = sanitized_model

            config["devices"][device_type].append(new_device)
            imported += 1

        except Exception as e:
            # Don't include potentially malicious IP in error message
            errors.append(f"Error importing device: {str(e)[:100]}")

    print(f"[IMPORT] Complete: imported={imported}, skipped={skipped}, errors={len(errors)}")
    if errors:
        print(f"[IMPORT] Errors: {errors}")

    if imported > 0:
        save_config(config)
        print(f"[IMPORT] Saved config with {imported} new miners")

    return imported, skipped, errors


def sync_miners_to_sentinel():
    """
    Sync miners from dashboard configuration to Sentinel's unified database.
    This ensures Sentinel can monitor miners that were added via the dashboard.
    Returns tuple of (synced_count, errors)
    """
    import re
    from datetime import datetime

    # IP address validation regex
    IP_PATTERN = re.compile(r'^(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$')

    # Sentinel database locations (try multiple paths)
    install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
    possible_paths = [
        Path(install_dir) / "data" / "miners.json",
        Path("/spiralpool/data/miners.json"),
    ]

    # On Windows, also check common locations
    if os.name == 'nt':
        possible_paths.insert(0, Path(os.environ.get("PROGRAMDATA", "C:/ProgramData")) / "SpiralPool" / "miners.json")
        possible_paths.insert(0, Path(os.environ.get("LOCALAPPDATA", "")) / "SpiralPool" / "miners.json")

    # Find or create Sentinel database
    sentinel_db_path = None
    for path in possible_paths:
        try:
            if path.exists():
                sentinel_db_path = path
                break
            # Try to create the directory if it doesn't exist
            if path.parent.exists() or str(path.parent).startswith("/spiralpool"):
                sentinel_db_path = path
                break
        except (PermissionError, OSError):
            continue

    if not sentinel_db_path:
        # Default to first path, will create directory
        sentinel_db_path = possible_paths[0]

    # Map dashboard types to sentinel types (must include ALL supported types)
    # BUGFIX: Moved ABOVE sentinel_data init — was previously defined 20 lines later,
    # causing UnboundLocalError ("cannot access local variable 'type_mapping'") which
    # meant sync_miners_to_sentinel() ALWAYS crashed and miners never reached miners.json.
    type_mapping = {
        # AxeOS HTTP API devices
        "axeos": "axeos",
        "bitaxe": "bitaxe",
        "nmaxe": "nmaxe",
        "nerdaxe": "nerdaxe",
        "nerdqaxe": "nerdqaxe",
        "nerdoctaxe": "nerdoctaxe",
        "qaxe": "qaxe",
        "qaxeplus": "qaxeplus",
        "luckyminer": "luckyminer",
        "jingleminer": "jingleminer",
        "zyber": "zyber",
        "esp32miner": "esp32miner",
        "hammer": "hammer",
        "goldshell": "goldshell",
        # CGMiner API devices
        "avalon": "avalon",
        "antminer": "antminer",
        "antminer_scrypt": "antminer_scrypt",
        "whatsminer": "whatsminer",
        "innosilicon": "innosilicon",
        "futurebit": "futurebit",
        "canaan": "canaan",
        "ebang": "ebang",
        "gekkoscience": "gekkoscience",
        "ipollo": "ipollo",
        "epic": "epic",
        "elphapex": "elphapex",
        # Custom firmware REST API
        "braiins": "braiins",
        "vnish": "vnish",
        "luxos": "luxos",
    }

    # Load existing Sentinel database or create new one
    # Initialize with ALL supported types to match sentinel's DEFAULT_MINERS
    sentinel_data = {
        "miners": {},
        "by_type": {k: [] for k in type_mapping.keys()},
        "last_scan": None
    }

    try:
        if sentinel_db_path.exists():
            with open(sentinel_db_path, 'r') as f:
                sentinel_data = json.load(f)
                # Ensure required keys exist
                if "miners" not in sentinel_data:
                    sentinel_data["miners"] = {}
                if "by_type" not in sentinel_data:
                    sentinel_data["by_type"] = {}
    except (json.JSONDecodeError, IOError, OSError) as e:
        print(f"[SYNC] Warning: Could not load existing Sentinel database: {e}")

    # Load dashboard config
    config = load_config()
    devices = config.get("devices", {})

    synced = 0
    errors = []

    # Clear by_type lists to rebuild from current config
    for mtype in sentinel_data["by_type"]:
        sentinel_data["by_type"][mtype] = []

    for device_type, device_list in devices.items():
        if not isinstance(device_list, list):
            continue

        sentinel_type = type_mapping.get(device_type)
        if not sentinel_type:
            continue

        # Ensure by_type has this type
        if sentinel_type not in sentinel_data["by_type"]:
            sentinel_data["by_type"][sentinel_type] = []

        for device in device_list:
            if not isinstance(device, dict):
                continue

            ip = device.get("ip", "")
            if not IP_PATTERN.match(ip):
                errors.append(f"Invalid IP: {ip[:50]}")
                continue

            # Get or sanitize nickname
            nickname = device.get("nickname") or device.get("name") or ""
            if nickname:
                nickname = re.sub(r'[^a-zA-Z0-9 ._-]', '', str(nickname)[:64])

            # Update or create miner entry
            if ip not in sentinel_data["miners"]:
                sentinel_data["miners"][ip] = {
                    "type": sentinel_type,
                    "added": datetime.utcnow().isoformat() + "Z",
                }

            # Always update these fields
            sentinel_data["miners"][ip]["type"] = sentinel_type
            sentinel_data["miners"][ip]["last_seen"] = datetime.utcnow().isoformat() + "Z"
            if nickname:
                sentinel_data["miners"][ip]["nickname"] = nickname

            # Add watts if specified
            if device.get("watts"):
                sentinel_data["miners"][ip]["watts"] = device["watts"]

            # Add fallback hashrate values if specified
            if device.get("fallback_ths"):
                sentinel_data["miners"][ip]["fallback_ths"] = device["fallback_ths"]
            if device.get("fallback_ghs"):
                sentinel_data["miners"][ip]["fallback_ghs"] = device["fallback_ghs"]

            # Add to by_type list if not already present
            if ip not in sentinel_data["by_type"][sentinel_type]:
                sentinel_data["by_type"][sentinel_type].append(ip)

            synced += 1

    # Update last_scan timestamp
    sentinel_data["last_scan"] = datetime.utcnow().isoformat() + "Z"

    # Write to Sentinel database
    try:
        # Create directory if needed
        sentinel_db_path.parent.mkdir(parents=True, exist_ok=True)

        _atomic_json_save(str(sentinel_db_path), sentinel_data, indent=2)

        print(f"[SYNC] Synced {synced} miners to Sentinel database: {sentinel_db_path}")

        # Signal Sentinel to reload (create a trigger file)
        reload_trigger = sentinel_db_path.parent / ".reload_miners"
        reload_ack = sentinel_db_path.parent / ".reload_ack"

        # P2 AUDIT FIX: Delete old ACK file before triggering reload
        # This ensures we can detect fresh ACK from the new reload
        try:
            if reload_ack.exists():
                reload_ack.unlink()
        except (PermissionError, OSError):
            pass  # Non-critical

        try:
            reload_trigger.touch()
            print(f"[SYNC] Triggered Sentinel reload")
        except (PermissionError, OSError):
            pass  # Non-critical if we can't create trigger file

    except (PermissionError, OSError) as e:
        errors.append(f"Could not write Sentinel database: {e}")
        print(f"[SYNC] Error writing Sentinel database: {e}")

    return synced, errors


def save_sentinel_expected_hashrate(expected_ths):
    """
    Save expected_fleet_ths to the Sentinel config file.

    Args:
        expected_ths: Expected hashrate in TH/s, or None to disable (use 1.0 as fallback)

    The sentinel config file is typically at ~/.spiralsentinel/config.json
    """
    # Find sentinel config file
    # BUGFIX: Check the systemd-safe fallback path FIRST.  When the dashboard runs
    # under systemd with ProtectHome=yes, ~/.spiralsentinel/ is on an empty tmpfs
    # and writes there silently vanish or raise PermissionError.
    install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
    possible_paths = [
        Path(install_dir) / "config" / "sentinel" / "config.json",   # systemd-safe fallback
        Path.home() / ".spiralsentinel" / "config.json",
    ]

    # Try to find pool user's home for Linux systems
    if os.name != 'nt':
        try:
            import pwd
            for username in ["spiralpool", "pool", "mining"]:
                try:
                    user_home = pwd.getpwnam(username).pw_dir
                    possible_paths.append(Path(user_home) / ".spiralsentinel" / "config.json")
                except KeyError:
                    pass
        except ImportError:
            pass

    # Find existing config or use default path
    sentinel_config_path = None
    for path in possible_paths:
        try:
            if path.exists():
                sentinel_config_path = path
                break
        except (PermissionError, OSError):
            continue

    if not sentinel_config_path:
        # Use the systemd-safe fallback (not home dir which ProtectHome blocks)
        sentinel_config_path = Path(install_dir) / "config" / "sentinel" / "config.json"

    # Load existing config or create new
    sentinel_config = {}
    if sentinel_config_path.exists():
        try:
            with open(sentinel_config_path, 'r') as f:
                sentinel_config = json.load(f)
        except (json.JSONDecodeError, IOError, OSError):
            sentinel_config = {}

    # Update expected_fleet_ths
    # If null/None, use 1.0 as a fallback (effectively disabled - very low threshold)
    # Also set a flag to indicate if feature is disabled
    if expected_ths is None or expected_ths <= 0:
        # User chose to disable/skip - use 1.0 TH/s as a low fallback
        sentinel_config["expected_fleet_ths"] = 1.0
        sentinel_config["expected_fleet_ths_disabled"] = True
        print(f"[CONFIG] Expected hashrate disabled (using 1.0 TH/s fallback)")
    else:
        sentinel_config["expected_fleet_ths"] = float(expected_ths)
        sentinel_config["expected_fleet_ths_disabled"] = False
        print(f"[CONFIG] Expected hashrate set to {expected_ths} TH/s")

    # Write config
    try:
        sentinel_config_path.parent.mkdir(parents=True, exist_ok=True)
        _atomic_json_save(str(sentinel_config_path), sentinel_config, indent=2)
        print(f"[CONFIG] Saved sentinel config to {sentinel_config_path}")
    except (PermissionError, OSError) as e:
        print(f"[CONFIG] Failed to save sentinel config: {e}")
        raise


def load_stats():
    """Load lifetime stats from file"""
    global lifetime_stats
    ensure_config_dir()
    with _lifetime_stats_lock:
        if STATS_FILE.exists():
            try:
                with open(STATS_FILE, 'r') as f:
                    loaded = json.load(f)
                    # Merge with defaults to ensure all fields exist
                    lifetime_stats.update(loaded)
            except Exception as e:
                print(f"Error loading stats: {e}")

        # Always ensure uptime_start is set (for new installs or corrupted files)
        if not lifetime_stats.get("uptime_start"):
            lifetime_stats["uptime_start"] = time.time()
            save_stats()


def save_stats():
    """Save lifetime stats to file"""
    ensure_config_dir()
    _atomic_json_save(str(STATS_FILE), lifetime_stats, indent=2)


# ============================================
# BLOCKCHAIN DATA FUNCTIONS
# ============================================

def fetch_block_reward():
    """Fetch current block reward and price for the active coin(s)"""
    global block_reward_cache

    # Only refresh every 5 minutes (block reward changes slowly)
    with _block_reward_lock:
        if time.time() - block_reward_cache["last_update"] < 300:
            return block_reward_cache.copy()  # Return copy to prevent external modification

    # Get the primary active coin
    primary_coin = get_primary_coin()
    # Use first available coin as fallback if primary not found, otherwise empty dict
    fallback_node = next(iter(MULTI_COIN_NODES.values()), {}) if MULTI_COIN_NODES else {}
    coin_node = MULTI_COIN_NODES.get(primary_coin, fallback_node) if primary_coin else fallback_node

    block_reward_cache["coin"] = primary_coin
    block_reward_cache["coin_name"] = coin_node.get("name", primary_coin)
    block_reward_cache["block_time"] = COIN_BLOCK_TIMES.get(primary_coin, 600)

    block_reward = None

    # Method 1: Try to get from pool API first (most reliable for our pool)
    try:
        response = _http_session.get(
            f"{POOL_API_URL}/api/pools",
            timeout=5
        )
        if response.status_code == 200:
            data = response.json()
            if data.get("pools") and len(data["pools"]) > 0:
                pool = data["pools"][0]
                # Pool API returns blockReward in coin units (already converted from satoshis)
                block_stats = pool.get("poolStats", {})
                if block_stats.get("blockReward"):
                    block_reward = block_stats["blockReward"]
                    block_reward_cache["block_height"] = block_stats.get("blockHeight", 0)
    except (requests.exceptions.RequestException, ValueError, KeyError, TypeError):
        pass

    # Method 2: Try to get from node RPC
    if block_reward is None:
        try:
            bc_info = coin_rpc(primary_coin, "getblockchaininfo")
            if bc_info:
                block_reward_cache["block_height"] = bc_info.get("blocks", 0)

            # Try to get the actual reward from a recent block
            blocks_resp = _http_session.get(
                f"{POOL_API_URL}/api/pools/{get_pool_id()}/blocks?pageSize=1",
                timeout=5
            )
            if blocks_resp.status_code == 200:
                blocks = blocks_resp.json()
                if blocks and len(blocks) > 0 and blocks[0].get("reward"):
                    # Reward is already in coin units (stratum converts from satoshis)
                    block_reward = blocks[0]["reward"]
        except (requests.exceptions.RequestException, ValueError, KeyError):
            pass

    # Method 3: Fetch live from blockchain APIs (calculates from block height)
    if block_reward is None:
        live_data = fetch_live_block_reward(primary_coin)
        block_reward = live_data.get("block_reward", COIN_BLOCK_REWARDS.get(primary_coin, 3.125))
        if live_data.get("block_height"):
            block_reward_cache["block_height"] = live_data["block_height"]

    # Method 4: Use known default values per coin (ultimate fallback)
    if block_reward is None or block_reward == 0:
        block_reward = COIN_BLOCK_REWARDS.get(primary_coin, 3.125)

    # Fetch coin price (all supported fiat currencies) from CoinGecko before locking
    coingecko_id = COINGECKO_IDS.get(primary_coin) if primary_coin else None
    coin_prices = {}
    if coingecko_id:
        try:
            response = _http_session.get(
                f"https://api.coingecko.com/api/v3/simple/price?ids={coingecko_id}&vs_currencies={DASHBOARD_VS_CURRENCIES}",
                timeout=5
            )
            data = response.json()
            coin_data = data.get(coingecko_id, {})
            for cur_code in DASHBOARD_VS_CURRENCIES.split(","):
                coin_prices[cur_code] = coin_data.get(cur_code, 0)
        except (requests.exceptions.RequestException, ValueError, KeyError):
            pass

    # Update cache atomically with lock
    with _block_reward_lock:
        block_reward_cache["block_reward"] = round(block_reward, 8)
        # Store all currency prices with price_{code} keys
        for cur_code, price_val in coin_prices.items():
            block_reward_cache[f"price_{cur_code}"] = price_val
        # Keep backward-compat keys — preserve last-known prices on CoinGecko failure
        if "usd" in coin_prices:
            block_reward_cache["price_usd"] = coin_prices["usd"]
        if "cad" in coin_prices:
            block_reward_cache["price_cad"] = coin_prices["cad"]
        block_reward_cache["last_update"] = time.time()
        return block_reward_cache.copy()


# ============================================
# SUBNET SCANNER FUNCTIONS
# ============================================

_cached_subnet = None
_cached_local_ip = None

def get_local_subnet():
    """Detect the local subnet to scan"""
    global _cached_subnet, _cached_local_ip

    # Return cached result if available
    if _cached_subnet and _cached_local_ip:
        return _cached_subnet, _cached_local_ip

    try:
        # Create a socket to determine local IP
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.settimeout(2.0)  # 2 second timeout
        s.connect(("8.8.8.8", 80))
        local_ip = s.getsockname()[0]
        s.close()

        # Create /24 subnet from local IP
        parts = local_ip.split('.')
        subnet = f"{parts[0]}.{parts[1]}.{parts[2]}.0/24"

        # Cache the result
        _cached_subnet = subnet
        _cached_local_ip = local_ip

        return subnet, local_ip
    except Exception as e:
        print(f"Error detecting subnet: {e}")
        return "192.168.1.0/24", "192.168.1.1"


def quick_port_check(ip, port, timeout=2.0):
    """Quick check if a port is open

    Args:
        ip: Target IP address
        port: Target port number
        timeout: Connection timeout in seconds (default 2.0 for reliable device detection)
    """
    sock = None
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(timeout)
        result = sock.connect_ex((ip, port))
        if result == 0:
            return True
        return False
    except (socket.error, socket.timeout, OSError) as e:
        return False
    finally:
        if sock:
            try:
                sock.close()
            except OSError:
                pass


def identify_miner(ip, timeout=5):
    """Try to identify what type of miner is at this IP

    Args:
        ip: Target IP address
        timeout: HTTP request timeout in seconds (default 5 for reliable detection)

    Note: Total time per host can be up to (port_check_timeout + http_timeout) seconds.
    With 50 parallel workers and 254 hosts, worst case scan time is about 90 seconds.
    """
    result = {
        "ip": ip,
        "found": False,
        "type": None,
        "name": None,
        "hashrate": None,
        "model": None
    }

    # Check for AxeOS/NerdQAxe HTTP API (port 80)
    # Use 2.5s port check timeout to ensure slow devices are detected
    if quick_port_check(ip, 80, 2.5):
        try:
            response = requests.get(
                f"http://{ip}/api/system/info",
                timeout=timeout,
                headers={"User-Agent": "SpiralPool-Scanner/1.0"}
            )
            if response.status_code == 200:
                data = response.json()

                # Determine miner type from response
                # Use boardVersion as primary identifier (more reliable than hostname)
                hostname = data.get('hostname', '').lower()
                version = data.get('version', '').lower()
                board_version = data.get('boardVersion', data.get('board', '')).lower()
                asic_model = data.get('ASICModel', '').lower()
                # Check if stratum URL format gives us a hint - NerdQAxe uses hostname-only
                stratum_url = data.get('stratumURL', '').lower()
                is_hostname_only_stratum = stratum_url and not stratum_url.startswith("stratum")

                # Check if this is an AxeOS-style API response (has hashRate field)
                if data.get('hashRate') is not None:
                    result["found"] = True
                    result["name"] = data.get('hostname', f"Miner_{ip.split('.')[-1]}")
                    # Validate and convert hashRate to numeric value
                    hashrate_val = data.get('hashRate', 0)
                    if isinstance(hashrate_val, (int, float)):
                        result["hashrate"] = hashrate_val
                    elif isinstance(hashrate_val, str):
                        try:
                            result["hashrate"] = float(hashrate_val)
                        except ValueError:
                            result["hashrate"] = 0
                    else:
                        result["hashrate"] = 0

                    # Determine specific device type based on boardVersion first
                    # Hammer Miner detection (Scrypt - DOGE/LTC)
                    # PlebSource Hammer Miner: 105 MH/s Scrypt, 25W, WiFi
                    # Uses AxeOS-style API - only detect actual Hammer Miners, not hostnames containing "hammer"
                    # Check boardVersion (authoritative) or specific PlebSource identifiers
                    if ('plebsource' in board_version or 'plebsource' in hostname or
                        'hammer miner' in board_version or 'hammer-miner' in board_version or
                        'hammerminer' in board_version or
                        (board_version == 'hammer' or hostname == 'hammer')):  # Exact match only for generic "hammer"
                        result["type"] = "hammer"  # Dedicated Hammer Miner type
                        result["model"] = "Hammer Miner"

                    # ESP32 Miner detection (ESP32-based solo miner)
                    # ESP32 Miner: Pure ESP32, ~50-78 kH/s SHA256, 1-2W
                    # Runs custom ESP32 Miner firmware (not AxeOS)
                    # Identified by: hostname/boardVersion containing "esp32miner"
                    # Hashrate is in kH/s range (very low compared to ASIC devices)
                    elif ('esp32miner' in board_version or 'esp32miner' in hostname or
                          'nerd-miner' in board_version or 'nerd-miner' in hostname or
                          'nerd_miner' in board_version or 'nerd_miner' in hostname):
                        # Distinguish from NerdAxe/NerdQAxe (which have ASIC chips)
                        # ESP32 Miner has no ASIC, very low hashrate
                        result["type"] = "esp32miner"
                        if 'v2' in board_version or 'v2' in hostname:
                            result["model"] = "ESP32 Miner"
                        elif 't-display' in board_version or 't-display' in hostname or 'tdisplay' in board_version:
                            result["model"] = "ESP32 T-Display Miner"
                        else:
                            result["model"] = "ESP32 Miner"

                    # QAxe / QAxe+ detection (quad-ASIC miners)
                    # QAxe: 4x BM1366 chips, ~2 TH/s
                    # QAxe+: Enhanced version with better cooling
                    # CRITICAL: Must exclude 'nerd' prefix to avoid matching NerdQAxe/NerdAxe!
                    # "qaxe" is a substring of "nerdqaxe", so we must be specific
                    elif (('qaxe' in board_version or 'qaxe' in hostname) and
                          'nerd' not in board_version and 'nerd' not in hostname):
                        if 'plus' in board_version or 'plus' in hostname or '+' in board_version:
                            result["type"] = "qaxeplus"
                            result["model"] = "QAxe+"
                        else:
                            result["type"] = "qaxe"
                            result["model"] = "QAxe"

                    # Lucky Miner detection (SHA-256d)
                    # LV06: 500 GH/s @ 13W, LV07: 1 TH/s @ 30W, LV08: 4.5 TH/s @ 120W
                    # BM1366 chip, ESP-Miner firmware (AxeOS-style API)
                    elif ('lucky' in board_version or 'lucky' in hostname or
                          'lv06' in board_version or 'lv07' in board_version or 'lv08' in board_version or
                          'lv06' in hostname or 'lv07' in hostname or 'lv08' in hostname):
                        result["type"] = "luckyminer"
                        if 'lv08' in board_version or 'lv08' in hostname:
                            result["model"] = "Lucky Miner LV08"
                        elif 'lv07' in board_version or 'lv07' in hostname:
                            result["model"] = "Lucky Miner LV07"
                        elif 'lv06' in board_version or 'lv06' in hostname:
                            result["model"] = "Lucky Miner LV06"
                        else:
                            result["model"] = "Lucky Miner"

                    # Jingle Miner detection (SHA-256d)
                    # BTC Solo Lite: 1.2 TH/s @ 23W, BTC Solo Pro: 4.8 TH/s @ 96W
                    # BM1370 chip, ESP-Miner based firmware (AxeOS-style API)
                    elif ('jingle' in board_version or 'jingle' in hostname or
                          'btc solo' in board_version or 'btc solo' in hostname or
                          'jingleminer' in board_version or 'jingleminer' in hostname):
                        result["type"] = "jingleminer"
                        if 'pro' in board_version or 'pro' in hostname:
                            result["model"] = "Jingle Miner BTC Solo Pro"
                        elif 'lite' in board_version or 'lite' in hostname:
                            result["model"] = "Jingle Miner BTC Solo Lite"
                        else:
                            result["model"] = "Jingle Miner"

                    # Zyber miner detection (SHA-256d) - TinyChipHub
                    # Zyber 8S: 6.4 TH/s @ 140W, 8x BM1368 chips
                    # Zyber 8G: 10+ TH/s @ 180W, 8x BM1370 chips
                    # Uses ESP-Miner/AxeOS firmware (TinyChipHub fork)
                    elif ('zyber' in board_version or 'zyber' in hostname or
                          'tinychip' in board_version or 'tinychip' in hostname):
                        result["type"] = "zyber"
                        if '8g' in board_version or '8g' in hostname:
                            result["model"] = "Zyber 8G"
                        elif '8s' in board_version or '8s' in hostname:
                            result["model"] = "Zyber 8S"
                        elif '8gp' in board_version or '8gp' in hostname:
                            result["model"] = "Zyber 8GP"
                        else:
                            result["model"] = "Zyber Miner"

                    # NerdOctaxe Gamma detection - MUST check BEFORE NerdQAxe++
                    # NerdOctaxe Gamma: 8x BM1370 chips, 9.6 TH/s, 160W
                    # Runs ESP-Miner-Nerd firmware (modified AxeOS)
                    elif ('nerdoctaxe' in board_version or 'nerdoctaxe' in hostname or
                        'octaxe' in board_version or 'octaxe' in hostname):
                        result["type"] = "nerdqaxe"  # Same category as NerdQAxe for config
                        result["model"] = "NerdOctaxe Gamma"

                    # BitAxe GT (Gamma Turbo) 801 - MUST check BEFORE NerdQAxe
                    # GT uses BM1370 chips but is NOT a NerdQAxe device
                    # GT has 2x BM1370 chips, ~2.15 TH/s, 43W
                    elif ('gt' in board_version or '801' in board_version or
                          'turbo' in board_version or 'gamma turbo' in board_version):
                        result["type"] = "axeos"
                        result["model"] = "BitAxe GT 801"

                    # NerdQAxe++ detection - check AFTER BitAxe GT to avoid misidentification
                    # NerdQAxe++ uses BM1370 but has specific identifiers in board/hostname
                    # IMPORTANT: Do NOT use broad checks like 'bm1370' alone - many devices use BM1370
                    # Also detect by hostname-only stratum URL format (no stratum+tcp:// prefix)
                    elif ('nerdqaxe' in board_version or 'nerdqaxe' in hostname or
                          'nerdaxe' in board_version or 'nerdaxe' in hostname or
                          is_hostname_only_stratum):
                        result["type"] = "nerdqaxe"
                        if 'nerdqaxe++' in board_version or 'nerdqaxe++' in hostname:
                            result["model"] = "NerdQAxe++"
                        elif 'nerdaxe' in board_version or 'nerdaxe' in hostname:
                            result["model"] = "NerdAxe"
                        else:
                            result["model"] = "NerdQAxe++"

                    # NMaxe detection - boardVersion: "NMAxe", hwModel: "NMAxe",
                    # or any known NMaxe board name (e.g., "BitRazor").
                    # Also detect by nested stratum dict (NMaxe v2.9+ schema marker).
                    elif ('nmaxe' in board_version or 'nmax' in board_version or
                          str(data.get('hwModel', '')).upper() == 'NMAXE' or
                          'bitrazor' in board_version or 'bitrazor' in hostname or
                          isinstance(data.get('stratum'), dict)):
                        result["type"] = "nmaxe"
                        # Use the specific board/model name if available
                        hw_model = data.get('hwModel', '')
                        if 'bitrazor' in board_version or 'bitrazor' in hostname:
                            result["model"] = "BitRazor"
                        elif hw_model and hw_model.upper() != 'NMAXE':
                            result["model"] = hw_model  # e.g., board-specific name
                        else:
                            result["model"] = "NMaxe"

                    elif 'gamma' in board_version:
                        result["type"] = "axeos"
                        if '102' in board_version:
                            result["model"] = "BitAxe Gamma 102"
                        elif '101' in board_version:
                            result["model"] = "BitAxe Gamma 101"
                        else:
                            result["model"] = "BitAxe Gamma"

                    elif 'ultra' in board_version:
                        result["type"] = "axeos"
                        result["model"] = "BitAxe Ultra"

                    elif 'hex' in board_version:
                        result["type"] = "axeos"
                        result["model"] = "BitAxe Hex"

                    elif 'supra' in board_version:
                        result["type"] = "axeos"
                        result["model"] = "BitAxe Supra"

                    # Generic AxeOS device - identify by ASIC chip if possible
                    else:
                        result["type"] = "axeos"
                        if asic_model:
                            result["model"] = f"AxeOS ({asic_model})"
                        else:
                            result["model"] = "AxeOS Device"

        except Exception as e:
            # Reset found flag if detection failed partway through
            # This prevents partial detections (found=True but type/model=None)
            result["found"] = False
            result["type"] = None
            result["model"] = None
            print(f"[SCAN] AxeOS identification error for {ip}: {e}")

    # Check for Goldshell HTTP API (port 80)
    # Goldshell miners use /mcb/status endpoint which returns model info
    if not result["found"] and quick_port_check(ip, 80, 2.5):
        try:
            response = requests.get(
                f"http://{ip}/mcb/status",
                timeout=timeout,
                headers={"User-Agent": "SpiralPool-Scanner/1.0"}
            )
            if response.status_code == 200:
                data = response.json()
                # Goldshell API returns 'model' field (e.g., "KD6", "LT5", "Mini-DOGE")
                model = data.get('model', '')
                if model:
                    result["found"] = True
                    result["type"] = "goldshell"
                    result["name"] = f"Goldshell_{model}_{ip.split('.')[-1]}"
                    result["model"] = f"Goldshell {model}"

                    # Try to get hashrate from cgminer endpoint
                    try:
                        devs_response = requests.get(
                            f"http://{ip}/mcb/cgminer?cgminercmd=summary",
                            timeout=timeout,
                            headers={"User-Agent": "SpiralPool-Scanner/1.0"}
                        )
                        if devs_response.status_code == 200:
                            devs_data = devs_response.json()
                            # Goldshell returns hashrate in MH/s for Scrypt miners
                            if 'data' in devs_data and isinstance(devs_data['data'], dict):
                                summary = devs_data['data']
                                # Try GHS first, then MHS
                                ghs = summary.get('GHS av', summary.get('GHS 5s', 0))
                                if ghs and float(ghs) > 0:
                                    result["hashrate"] = float(ghs)
                                else:
                                    mhs = summary.get('MHS av', summary.get('MHS 5s', 0))
                                    if mhs:
                                        result["hashrate"] = float(mhs) / 1000  # Convert to GH/s
                    except Exception as e:
                        print(f"[SCAN] Goldshell hashrate fetch error for {ip}: {e}")
        except Exception as e:
            print(f"[SCAN] Goldshell identification error for {ip}: {e}")

    # Check port 4028 — used by both ePIC (HTTP REST) and CGMiner (TCP socket)
    port_4028_open = not result["found"] and quick_port_check(ip, 4028, 2.5)

    # Try ePIC BlockMiner HTTP REST API FIRST (ePIC uses HTTP on port 4028, NOT CGMiner TCP)
    if port_4028_open and not result["found"]:
        try:
            epic_resp = requests.get(f"http://{ip}:4028/summary", auth=("root", "letmein"), timeout=timeout)
            if epic_resp.status_code == 200:
                epic_data = epic_resp.json()
                if epic_data.get("Mining") or "ePIC" in str(epic_data) or "BlockMiner" in str(epic_data):
                    result["found"] = True
                    result["type"] = "epic"
                    result["model"] = "ePIC BlockMiner"
                    mining = epic_data.get("Mining", {})
                    ghs = float(mining.get("Speed(GHS)", 0) or 0)
                    result["hashrate"] = f"{ghs:.2f} GH/s" if ghs < 1000 else f"{ghs/1000:.2f} TH/s"
                    print(f"[SCAN] Found ePIC BlockMiner at {ip} (HTTP REST on 4028)")
        except Exception:
            pass  # Not ePIC — fall through to CGMiner TCP probe below

    # Check for CGMiner API (port 4028) - used by Avalon, Antminer, Whatsminer, Innosilicon
    if port_4028_open and not result["found"]:
        # Helper to safely execute CGMiner API command with proper socket cleanup
        def cgminer_command(command):
            sock = None
            try:
                sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
                sock.settimeout(timeout)
                sock.connect((ip, 4028))
                sock.sendall(command)
                response = b""
                while True:
                    chunk = sock.recv(4096)
                    if not chunk:
                        break
                    response += chunk
                    if b'\x00' in chunk:
                        break
                return response.decode('utf-8', errors='ignore').replace('\x00', '')
            except (socket.error, socket.timeout, OSError):
                return None
            finally:
                if sock:
                    try:
                        sock.close()
                    except OSError:
                        pass

        try:
            # Try CGMiner API version command to identify miner type
            response_str = cgminer_command(b'{"command":"version"}')

            # Check if it's a valid CGMiner response
            if response_str and ('CGMiner' in response_str or 'STATUS' in response_str or 'Miner' in response_str):
                result["found"] = True

                # Try to get stats to identify miner type more accurately
                miner_type = "avalon"  # Default to Avalon for unknown CGMiner devices
                model_name = "Unknown ASIC"

                stats_str = cgminer_command(b'{"command":"stats"}')
                if stats_str:
                    # Detect Bitmain Antminer by characteristic stats fields
                    if 'Antminer' in stats_str or 'bitmain' in stats_str.lower() or 'chain_rate' in stats_str:
                        # Check if this is a Scrypt Antminer (L-series: L3+, L7, L9)
                        # These mine LTC/DOGE and need different categorization
                        if 'L9' in stats_str:
                            miner_type = "antminer_scrypt"
                            model_name = "Antminer L9"
                        elif 'L7' in stats_str:
                            miner_type = "antminer_scrypt"
                            model_name = "Antminer L7"
                        elif 'L3' in stats_str:
                            miner_type = "antminer_scrypt"
                            model_name = "Antminer L3+"
                        # SHA-256d Antminers (S/T-series)
                        elif 'S21' in stats_str:
                            miner_type = "antminer"
                            model_name = "Antminer S21"
                        elif 'S19' in stats_str:
                            miner_type = "antminer"
                            if 'XP' in stats_str:
                                model_name = "Antminer S19 XP"
                            elif 'Pro' in stats_str:
                                model_name = "Antminer S19 Pro"
                            else:
                                model_name = "Antminer S19"
                        elif 'T21' in stats_str:
                            miner_type = "antminer"
                            model_name = "Antminer T21"
                        else:
                            miner_type = "antminer"
                            model_name = "Bitmain Antminer"

                    # Detect Whatsminer by characteristic patterns
                    elif 'Whatsminer' in stats_str or 'MicroBT' in stats_str.lower() or 'btminer' in stats_str.lower():
                        miner_type = "whatsminer"
                        if 'M60' in stats_str:
                            model_name = "Whatsminer M60"
                        elif 'M50' in stats_str:
                            model_name = "Whatsminer M50"
                        elif 'M30' in stats_str:
                            model_name = "Whatsminer M30"
                        else:
                            model_name = "MicroBT Whatsminer"

                    # Detect Innosilicon
                    elif 'Innosilicon' in stats_str or 'inno' in stats_str.lower():
                        miner_type = "innosilicon"
                        if 'A11' in stats_str:
                            model_name = "Innosilicon A11"
                        elif 'A10' in stats_str:
                            model_name = "Innosilicon A10"
                        elif 'T3' in stats_str:
                            model_name = "Innosilicon T3"
                        else:
                            model_name = "Innosilicon ASIC"

                    # Detect Avalon
                    elif 'Avalon' in stats_str or 'avalon' in stats_str.lower():
                        miner_type = "avalon"
                        model_name = "Avalon Miner"

                    # Detect FutureBit Apollo (SHA-256d)
                    # Apollo Gen1: 2-3.8 TH/s @ 125-200W
                    # Apollo II: 6-9 TH/s @ 175-375W
                    elif 'FutureBit' in stats_str or 'futurebit' in stats_str.lower() or 'Apollo' in stats_str:
                        miner_type = "futurebit"
                        if 'II' in stats_str or 'Apollo II' in stats_str:
                            model_name = "FutureBit Apollo II"
                        else:
                            model_name = "FutureBit Apollo"

                    # Detect Canaan AvalonMiner (SHA-256d)
                    # A12/A13/A14 series - industrial ASIC miners
                    # Uses CGMiner-compatible API
                    elif 'Canaan' in stats_str or 'canaan' in stats_str.lower() or 'AvalonMiner' in stats_str:
                        miner_type = "canaan"
                        if 'A14' in stats_str or '1466' in stats_str:
                            model_name = "Canaan AvalonMiner A14"
                        elif 'A13' in stats_str or '1366' in stats_str:
                            model_name = "Canaan AvalonMiner A13"
                        elif 'A12' in stats_str or '1246' in stats_str:
                            model_name = "Canaan AvalonMiner A12"
                        else:
                            model_name = "Canaan AvalonMiner"

                    # Detect Ebang Ebit (SHA-256d)
                    # E12/E12+ series - industrial ASIC miners
                    # Uses CGMiner-compatible API
                    elif 'Ebang' in stats_str or 'ebang' in stats_str.lower() or 'Ebit' in stats_str or 'ebit' in stats_str.lower():
                        miner_type = "ebang"
                        if 'E12+' in stats_str or 'E12 Pro' in stats_str:
                            model_name = "Ebang Ebit E12+"
                        elif 'E12' in stats_str:
                            model_name = "Ebang Ebit E12"
                        elif 'E11' in stats_str:
                            model_name = "Ebang Ebit E11"
                        else:
                            model_name = "Ebang Ebit"

                    # Detect GekkoScience USB miners (SHA-256d)
                    # Compac F, NewPac, R606 - USB stick miners
                    # Uses CGMiner/BFGMiner API
                    elif 'GekkoScience' in stats_str or 'gekkoscience' in stats_str.lower() or 'compac' in stats_str.lower() or 'newpac' in stats_str.lower():
                        miner_type = "gekkoscience"
                        if 'Compac' in stats_str or 'compac' in stats_str.lower():
                            model_name = "GekkoScience Compac F"
                        elif 'NewPac' in stats_str or 'newpac' in stats_str.lower():
                            model_name = "GekkoScience NewPac"
                        elif 'R606' in stats_str:
                            model_name = "GekkoScience R606"
                        else:
                            model_name = "GekkoScience Miner"

                    # Detect iPollo miners
                    # V1, V1 Mini, G1 series
                    # Uses CGMiner-compatible API
                    elif 'iPollo' in stats_str or 'ipollo' in stats_str.lower():
                        miner_type = "ipollo"
                        if 'Mini' in stats_str or 'mini' in stats_str.lower():
                            model_name = "iPollo V1 Mini"
                        elif 'G1' in stats_str:
                            model_name = "iPollo G1"
                        elif 'V1' in stats_str:
                            model_name = "iPollo V1"
                        else:
                            model_name = "iPollo Miner"

                    # Detect ePIC BlockMiner
                    # NOTE: ePIC uses HTTP REST on port 4028, NOT CGMiner TCP socket
                    elif 'ePIC' in stats_str or 'epic' in stats_str.lower() or 'BlockMiner' in stats_str:
                        miner_type = "epic"
                        model_name = "ePIC BlockMiner"

                    # Detect Elphapex miners (Scrypt)
                    # DG1, DG Home series
                    # Uses CGMiner-compatible API
                    elif 'Elphapex' in stats_str or 'elphapex' in stats_str.lower() or 'DG1' in stats_str or 'DG Home' in stats_str:
                        miner_type = "elphapex"
                        if 'DG Home' in stats_str or 'DG-Home' in stats_str:
                            model_name = "Elphapex DG Home"
                        elif 'DG1' in stats_str:
                            model_name = "Elphapex DG1"
                        else:
                            model_name = "Elphapex Miner"

                result["type"] = miner_type
                result["name"] = f"{model_name.replace(' ', '_')}_{ip.split('.')[-1]}"
                result["model"] = model_name

                # Try to get hashrate from summary
                summary_str = cgminer_command(b'{"command":"summary"}')
                if summary_str:
                    try:
                        if '|' in summary_str:
                            summary_str = summary_str.split('|', 1)[1]
                        if summary_str.endswith('EOF'):
                            summary_str = summary_str[:-3]

                        data = json.loads(summary_str)
                        if 'SUMMARY' in data:
                            summary = data['SUMMARY'][0]
                            # Try GHS first (industrial miners), then MHS
                            ghs = summary.get('GHS av', summary.get('GHS 5s', 0))
                            if ghs and float(ghs) > 0:
                                result["hashrate"] = float(ghs)  # Already in GH/s
                            else:
                                mhs = summary.get('MHS av', summary.get('MHS 5s', 0))
                                result["hashrate"] = float(mhs) / 1000 if mhs else 0  # Convert to GH/s
                    except (json.JSONDecodeError, KeyError, IndexError, ValueError) as e:
                        print(f"[SCAN] CGMiner summary parse error for {ip}: {e}")

        except Exception as e:
            print(f"[SCAN] CGMiner identification error for {ip}: {e}")

    return result


def get_all_configured_ips():
    """Get all IPs that are already configured across all sources (dashboard config + miners.json)"""
    configured_ips = set()

    # 1. Get IPs from dashboard config
    try:
        config = load_config()
        devices = config.get("devices", {})

        # Handle both dict format (ip -> device) and list format (device_type -> [devices])
        if isinstance(devices, dict):
            for key, value in devices.items():
                if isinstance(value, list):
                    # List format: {"axeos": [{"ip": "..."}, ...]}
                    for device in value:
                        if isinstance(device, dict) and device.get("ip"):
                            configured_ips.add(device["ip"])
                elif isinstance(value, dict) and value.get("ip"):
                    # Dict format where key is IP: {"192.168.1.1": {...}}
                    configured_ips.add(key)
                elif isinstance(key, str) and key.count('.') == 3:
                    # Key itself is an IP
                    configured_ips.add(key)
    except Exception as e:
        print(f"[SCAN] Warning: Could not load dashboard config: {e}")

    # 2. Get IPs from miners.json database
    try:
        install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
        possible_paths = [
            Path(install_dir) / "data" / "miners.json",
            Path("/spiralpool/data/miners.json"),
        ]
        if os.name == 'nt':
            possible_paths.insert(0, Path(os.environ.get("PROGRAMDATA", "C:/ProgramData")) / "SpiralPool" / "miners.json")
            possible_paths.insert(0, Path(os.environ.get("LOCALAPPDATA", "")) / "SpiralPool" / "miners.json")

        for path in possible_paths:
            try:
                if path.exists():
                    with open(path, 'r') as f:
                        miner_db = json.load(f)
                    miners = miner_db.get("miners", {})
                    if isinstance(miners, dict):
                        configured_ips.update(miners.keys())
                    break
            except (PermissionError, OSError, json.JSONDecodeError):
                continue
    except Exception as e:
        print(f"[SCAN] Warning: Could not load miners.json: {e}")

    return configured_ips


def scan_subnet(subnet=None, progress_callback=None, found_callback=None, phase_callback=None):
    """Scan a subnet for miners

    Args:
        subnet: CIDR subnet to scan (auto-detected if None)
        progress_callback: Called with (scanned, total, current_ip) during scan
        found_callback: Called with (miner_result) when a miner is found - enables real-time updates
        phase_callback: Called with (phase, message) to update scan phase status
    """
    if subnet is None:
        subnet, _ = get_local_subnet()

    try:
        network = ipaddress.ip_network(subnet, strict=False)
    except ValueError as e:
        return {"error": str(e), "found": []}

    # Get already configured IPs to filter duplicates
    configured_ips = get_all_configured_ips()
    if configured_ips:
        print(f"[SCAN] Will skip {len(configured_ips)} already-configured IPs")

    hosts = list(network.hosts())
    found_miners = []
    scanned = [0]  # Use list for mutable reference in nested function
    total = len(hosts)
    responsive_hosts = [0]
    found_lock = threading.Lock()
    scanned_lock = threading.Lock()

    # Phase 1: Quick ARP/network warmup - send UDP packets to wake up the network
    # This helps populate the ARP cache so subsequent TCP connections are faster
    print(f"[SCAN] Phase 1: Network warmup for {len(hosts)} hosts...")
    def warmup_host(ip):
        try:
            # Send to multiple common ports to ensure ARP resolution
            sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            sock.settimeout(0.1)
            for port in [9, 80, 4028]:  # Discard, HTTP, CGMiner
                try:
                    sock.sendto(b'', (str(ip), port))
                except Exception:
                    pass
            sock.close()
        except Exception:
            pass

    with concurrent.futures.ThreadPoolExecutor(max_workers=100) as executor:
        list(executor.map(warmup_host, hosts))

    # Pause to let ARP cache fully populate before TCP scanning
    # This is critical for reliable device detection
    time.sleep(1.0)
    print("[SCAN] Phase 2: Scanning for miners (max 2 minutes)...")

    # Track hosts that had open ports but didn't respond to API (for retry)
    retry_candidates = []
    retry_lock = threading.Lock()

    def scan_host(ip, is_retry=False):
        ip_str = str(ip)
        try:
            result = identify_miner(ip_str)
        except Exception as e:
            # Catch any unexpected exceptions to prevent thread death
            print(f"[SCAN] Exception scanning {ip_str}: {e}")
            result = {"ip": ip_str, "found": False, "type": None, "name": None, "hashrate": None, "model": None}

        with scanned_lock:
            scanned[0] += 1
            current_scanned = scanned[0]

        if result["found"]:
            with scanned_lock:
                responsive_hosts[0] += 1
            print(f"[SCAN] Found miner at {ip_str}: type={result.get('type')}, name={result.get('name')}")

            # Mark if already configured
            miner_ip = result.get("ip", "")
            if miner_ip in configured_ips:
                result["already_configured"] = True
                print(f"[SCAN] Found (already configured): {miner_ip}")

            # Thread-safe append and real-time callback
            with found_lock:
                found_miners.append(result)
                if found_callback:
                    found_callback(result)
        elif not is_retry:
            # Check if port 80 or 4028 is open but miner wasn't detected
            # These are candidates for retry (device may have been slow to respond)
            try:
                if quick_port_check(ip_str, 80, 0.5) or quick_port_check(ip_str, 4028, 0.5):
                    with retry_lock:
                        retry_candidates.append(ip_str)
            except Exception:
                pass

        if progress_callback:
            progress_callback(current_scanned, total, ip_str)
        return result

    # Use thread pool for faster scanning
    # Max 2 minutes total scan time to ensure all hosts are checked
    MAX_SCAN_TIMEOUT = 90  # 90 seconds for first pass, leaving 30s for retry
    scan_start = time.time()

    with concurrent.futures.ThreadPoolExecutor(max_workers=50) as executor:
        futures = {executor.submit(scan_host, ip): ip for ip in hosts}

        # Wait for all futures with timeout, ensuring complete scan
        done, not_done = concurrent.futures.wait(
            futures,
            timeout=MAX_SCAN_TIMEOUT,
            return_when=concurrent.futures.ALL_COMPLETED
        )

        # Process completed futures
        for future in done:
            try:
                future.result()  # Catch any exceptions
            except Exception as e:
                print(f"[SCAN] Error scanning host: {e}")

        # Log if any timed out
        if not_done:
            elapsed = time.time() - scan_start
            print(f"[SCAN] WARNING: {len(not_done)} hosts still pending after {elapsed:.1f}s timeout")
            for future in not_done:
                future.cancel()

    first_pass_elapsed = time.time() - scan_start
    print(f"[SCAN] First pass complete in {first_pass_elapsed:.1f}s: found {len(found_miners)} miners, {len(retry_candidates)} retry candidates")

    # Phase 3: Retry pass for hosts that had open ports but didn't respond
    # These devices may have been slow to initialize or had temporary issues
    if retry_candidates and (time.time() - scan_start) < 100:  # Only if we have time left
        # Remove already-found IPs from retry list
        found_ips = {m.get("ip") for m in found_miners}
        retry_list = [ip for ip in retry_candidates if ip not in found_ips]

        if retry_list:
            print(f"[SCAN] Phase 3: Retrying {len(retry_list)} hosts with open ports...")

            # Update phase for UI
            if phase_callback:
                phase_callback("retry", f"Retrying {len(retry_list)} slow devices...")

            time.sleep(0.5)  # Brief pause before retry

            with concurrent.futures.ThreadPoolExecutor(max_workers=25) as executor:
                retry_futures = {executor.submit(scan_host, ip, True): ip for ip in retry_list}

                # Wait up to 25 seconds for retries
                done_retry, not_done_retry = concurrent.futures.wait(
                    retry_futures,
                    timeout=25,
                    return_when=concurrent.futures.ALL_COMPLETED
                )

                for future in done_retry:
                    try:
                        future.result()
                    except Exception as e:
                        print(f"[SCAN] Retry error: {e}")

                if not_done_retry:
                    for future in not_done_retry:
                        future.cancel()

            retry_elapsed = time.time() - scan_start - first_pass_elapsed
            print(f"[SCAN] Retry pass complete in {retry_elapsed:.1f}s")

    scan_elapsed = time.time() - scan_start
    print(f"[SCAN] Subnet scan complete in {scan_elapsed:.1f}s: {total} hosts scanned, {responsive_hosts[0]} responsive, {len(found_miners)} miners found")

    return {
        "subnet": subnet,
        "scanned": total,
        "found": found_miners
    }


# Scan progress tracking with enhanced status
scan_progress = {
    "running": False,
    "scanned": 0,
    "total": 0,
    "found": [],
    "error": None,
    "current_ip": None,       # Currently scanning IP
    "start_time": None,       # Scan start timestamp
    "heartbeat": None,        # Last activity timestamp (prevents false "stuck" detection)
    "phase": "idle",          # Phase: idle, initializing, scanning, retry, checking_sentinel, complete
    "phase_msg": ""           # Human-readable phase message
}
_scan_thread = None  # Reference to current scan thread


def run_background_scan(subnet=None):
    """Run subnet scan in background thread

    Note: State initialization (running, found, start_time, etc.) is done in
    start_scan() API endpoint BEFORE this thread starts. This prevents a race
    condition where the frontend could poll status before the thread initializes.
    """
    global scan_progress

    print(f"[SCAN] Background scan thread STARTED (running={scan_progress.get('running')})")

    try:
        # Only set phase info here - the rest was already set by start_scan()
        scan_progress["phase"] = "initializing"
        scan_progress["phase_msg"] = "Detecting network..."
        scan_progress["subnet"] = None
        scan_progress["current_ip"] = None

        def update_progress(scanned, total, current_ip=None):
            scan_progress["scanned"] = scanned
            scan_progress["total"] = total
            scan_progress["heartbeat"] = time.time()  # Update heartbeat on each progress
            if current_ip:
                scan_progress["current_ip"] = current_ip
            scan_progress["phase"] = "scanning"
            scan_progress["phase_msg"] = f"Scanning {current_ip}" if current_ip else "Scanning network..."

        def on_miner_found(miner_result):
            """Real-time callback when a miner is discovered - updates scan_progress immediately"""
            scan_progress["found"].append(miner_result)

        # Determine subnet first to set total count early
        if subnet is None:
            subnet, local_ip = get_local_subnet()
            print(f"[SCAN] Auto-detected subnet: {subnet} (local IP: {local_ip})")
        else:
            print(f"[SCAN] Using specified subnet: {subnet}")

        scan_progress["subnet"] = subnet

        try:
            network = ipaddress.ip_network(subnet, strict=False)
        except ValueError as e:
            print(f"[SCAN] Invalid subnet {subnet}: {e}")
            scan_progress["error"] = f"Invalid subnet: {e}"
            scan_progress["phase"] = "complete"
            scan_progress["phase_msg"] = f"Invalid subnet: {e}"
            return

        # SECURITY: Only allow scanning private networks to prevent SSRF
        if not network.is_private:
            print(f"[SCAN] Rejected non-private subnet: {subnet}")
            scan_progress["error"] = "Only private network subnets can be scanned (10.x.x.x, 172.16-31.x.x, 192.168.x.x)"
            scan_progress["phase"] = "complete"
            scan_progress["phase_msg"] = "Non-private subnet rejected"
            return

        scan_progress["total"] = len(list(network.hosts()))
        print(f"[SCAN] Scanning {scan_progress['total']} hosts in {subnet}")

        def on_phase_change(phase, message):
            """Update scan phase for UI feedback"""
            scan_progress["phase"] = phase
            scan_progress["phase_msg"] = message

        result = scan_subnet(subnet, update_progress, on_miner_found, on_phase_change)
        # found[] is already populated in real-time via on_miner_found callback
        # but set it from result as well for completeness
        if not scan_progress["found"]:
            scan_progress["found"] = result.get("found", [])
        found_count = len(scan_progress['found'])
        print(f"[SCAN] Scan complete. Found {found_count} miners.")

        # If first pass found nothing, do a quick retry pass
        # This helps with network timing issues where devices may not respond immediately
        if found_count == 0:
            print("[SCAN] No miners found on first pass, retrying...")
            scan_progress["phase"] = "retrying"
            scan_progress["phase_msg"] = "Retrying scan..."
            scan_progress["scanned"] = 0

            # Brief pause before retry to let network settle
            time.sleep(1.0)

            # Clear and retry
            scan_progress["found"] = []
            result = scan_subnet(subnet, update_progress, on_miner_found)
            if not scan_progress["found"]:
                scan_progress["found"] = result.get("found", [])
            found_count = len(scan_progress['found'])
            print(f"[SCAN] Retry pass complete. Found {found_count} miners.")

        if "error" in result:
            scan_progress["error"] = result["error"]
            scan_progress["phase"] = "error"
            scan_progress["phase_msg"] = result["error"]
            print(f"[SCAN] Scan error: {result['error']}")

        # NOTE: We do NOT auto-save miners here. The user must explicitly click
        # "Add All" or select devices and save configuration. This ensures the
        # user has control over which devices are added to their configuration.

        # If no miners found via network scan, check if there are any in the
        # sentinel database (miners.json) that we can display as suggestions
        # (but NOT auto-import - just show them for user selection)
        if found_count == 0:
            scan_progress["phase"] = "checking_sentinel"
            scan_progress["phase_msg"] = "Checking scanner database..."
            print("[SCAN] No miners found via network scan. Checking sentinel database...")
            try:
                # Read miners.json directly (don't call import_miners_from_sentinel which saves)
                install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
                possible_paths = [
                    Path(install_dir) / "data" / "miners.json",
                    Path("/spiralpool/data/miners.json"),
                ]
                if os.name == 'nt':
                    possible_paths.insert(0, Path(os.environ.get("PROGRAMDATA", "C:/ProgramData")) / "SpiralPool" / "miners.json")
                    possible_paths.insert(0, Path(os.environ.get("LOCALAPPDATA", "")) / "SpiralPool" / "miners.json")

                sentinel_db = None
                for path in possible_paths:
                    if path.exists():
                        sentinel_db = path
                        break

                if sentinel_db:
                    with open(sentinel_db, 'r') as f:
                        sentinel_data = json.load(f)
                    miners = sentinel_data.get("miners", {})
                    if miners and isinstance(miners, dict):
                        # Get already configured IPs to mark them
                        configured_ips = get_all_configured_ips()

                        for ip, miner_info in miners.items():
                            if isinstance(miner_info, dict):
                                miner_type = miner_info.get("type", "axeos")
                                # Map known aliases
                                if miner_type == "bitaxe":
                                    miner_type = "axeos"
                                scan_progress["found"].append({
                                    "ip": ip,
                                    "found": True,
                                    "type": miner_type,
                                    "name": miner_info.get("nickname") or miner_info.get("name") or f"{miner_type}_{ip.split('.')[-1]}",
                                    "model": miner_info.get("model") or miner_type,
                                    "from_sentinel": True,  # Mark as from database (not live scan)
                                    "already_configured": ip in configured_ips
                                })
                        found_count = len(scan_progress["found"])
                        if found_count > 0:
                            print(f"[SCAN] Found {found_count} miners in sentinel database (displayed for user selection)")
            except Exception as e:
                print(f"[SCAN] Error reading sentinel database: {e}")

        # Set final phase
        elapsed = time.time() - (scan_progress.get("start_time") or time.time())

        # Ensure minimum scan duration for UI feedback (prevents "flash" on fast networks)
        # Minimum 5 seconds to ensure UI has time to display progress
        MIN_SCAN_DURATION = 5.0  # seconds
        if elapsed < MIN_SCAN_DURATION:
            remaining = MIN_SCAN_DURATION - elapsed
            print(f"[SCAN] Scan completed in {elapsed:.1f}s, waiting {remaining:.1f}s for UI stability")
            time.sleep(remaining)
            elapsed = MIN_SCAN_DURATION

        if found_count > 0:
            # Count how many are already configured vs new
            already_configured = sum(1 for m in scan_progress["found"] if m.get("already_configured"))
            new_count = found_count - already_configured
            scan_progress["phase"] = "complete"
            if already_configured > 0 and new_count == 0:
                scan_progress["phase_msg"] = f"Found {found_count} device(s) (all already configured) in {elapsed:.1f}s"
            elif already_configured > 0:
                scan_progress["phase_msg"] = f"Found {found_count} device(s) ({new_count} new, {already_configured} existing) in {elapsed:.1f}s"
            else:
                scan_progress["phase_msg"] = f"Found {found_count} device(s) in {elapsed:.1f}s"
        else:
            scan_progress["phase"] = "complete"
            scan_progress["phase_msg"] = f"No devices found ({elapsed:.1f}s)"

    except Exception as e:
        import traceback
        error_msg = f"Scan failed: {e}"
        scan_progress["error"] = error_msg
        scan_progress["phase"] = "error"
        scan_progress["phase_msg"] = error_msg
        print(f"[SCAN] EXCEPTION: {e}")
        print(f"[SCAN] Traceback: {traceback.format_exc()}")
        try:
            app.logger.error(f"Exception during scan: {e}", exc_info=True)
        except Exception:
            pass  # Logger might not be available in thread context
    finally:
        scan_progress["running"] = False
        print("[SCAN] Background scan thread finished")


# ============================================
# MINER API FUNCTIONS
# ============================================

# BraiinsOS session cache for token reuse
_braiins_sessions = {}  # {ip: {"token": str, "expires": timestamp}}
_braiins_sessions_lock = threading.Lock()


def braiins_api_call(ip, endpoint, method="GET", data=None, username="root", password="", timeout=10):
    """Make authenticated API call to BraiinsOS miner.

    BraiinsOS REST API uses token-based authentication.
    Tokens are cached and reused until they expire.

    Args:
        ip: Miner IP address
        endpoint: API endpoint (e.g., "/miner/stats")
        method: HTTP method (GET, POST, PUT, DELETE)
        data: Optional JSON data for POST/PUT requests
        username: BraiinsOS username (default: root)
        password: BraiinsOS password
        timeout: Request timeout in seconds

    Returns:
        dict: API response or {"error": "message"}
    """
    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return {"error": "Invalid or blocked IP address (SSRF protection)"}

    base_url = f"http://{ip}/api/v1"

    # Check for cached valid token
    now = time.time()
    with _braiins_sessions_lock:
        session = _braiins_sessions.get(ip, {})
        token = session.get("token")
        expires = session.get("expires", 0)

    # Token expired or not present - authenticate
    if not token or now >= expires:
        try:
            auth_response = requests.post(
                f"{base_url}/auth/login",
                json={"username": username, "password": password},
                timeout=timeout
            )
            if auth_response.status_code == 200:
                auth_data = auth_response.json()
                token = auth_data.get("token")
                # Token expires after 3600 seconds of inactivity, we'll refresh at 3000
                with _braiins_sessions_lock:
                    _braiins_sessions[ip] = {
                        "token": token,
                        "expires": now + 3000
                    }
            else:
                return {"error": f"Authentication failed: {auth_response.status_code}"}
        except requests.exceptions.RequestException as e:
            return {"error": f"Auth request failed: {str(e)}"}

    # Make the actual API call
    headers = {"Authorization": f"Bearer {token}"} if token else {}

    try:
        if method == "GET":
            response = requests.get(f"{base_url}{endpoint}", headers=headers, timeout=timeout)
        elif method == "POST":
            response = requests.post(f"{base_url}{endpoint}", headers=headers, json=data, timeout=timeout)
        elif method == "PUT":
            response = requests.put(f"{base_url}{endpoint}", headers=headers, json=data, timeout=timeout)
        elif method == "DELETE":
            response = requests.delete(f"{base_url}{endpoint}", headers=headers, timeout=timeout)
        else:
            return {"error": f"Unsupported method: {method}"}

        if response.status_code == 204:
            return {"success": True}  # No content response (e.g., reboot)
        elif response.status_code == 200:
            return response.json()
        elif response.status_code == 401:
            # Token expired, clear cache and retry once
            with _braiins_sessions_lock:
                _braiins_sessions.pop(ip, None)
            return {"error": "Authentication expired, please retry"}
        else:
            return {"error": f"API returned {response.status_code}: {response.text[:200]}"}

    except requests.exceptions.RequestException as e:
        return {"error": str(e)}


def fetch_braiins(ip, username="root", password="", timeout=10):
    """Fetch data from BraiinsOS/BOS+ miner via Public REST API (v1).

    Supported devices: Any ASIC running BraiinsOS firmware
    (Antminer S9, S17, S19, S21, T17, T19, etc.)

    BraiinsOS REST API endpoints (base: /api/v1/):
    - /miner/details: Hardware model, firmware version, uptime
    - /miner/stats: Hashrate (GH/s), power (watt), pool stats
    - /cooling/state: Highest temperature (degree_c), fan RPMs
    - /pools/: Pool configuration

    Auth: POST /api/v1/auth/login → Bearer token
    Reference: https://developer.braiins-os.com/latest/openapi.html
    """
    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return {
            "online": False,
            "error": "Invalid or blocked IP address (SSRF protection)"
        }

    try:
        # Get miner details (model, version, uptime)
        details = braiins_api_call(ip, "/miner/details", username=username, password=password, timeout=timeout)
        if details.get("error"):
            details = {"error": details.get("error")}

        # Get mining stats (hashrate, power, shares)
        stats = braiins_api_call(ip, "/miner/stats", username=username, password=password, timeout=timeout)

        # Get cooling state (temperatures, fans)
        cooling = braiins_api_call(ip, "/cooling/state", username=username, password=password, timeout=timeout)

        # Get pool configuration
        pools = braiins_api_call(ip, "/pools/", username=username, password=password, timeout=timeout)

        # Check if we got valid data
        if stats.get("error") and cooling.get("error"):
            raise Exception(stats.get("error") or cooling.get("error"))

        # Extract hashrate from stats
        # BOS+ API v1: miner_stats.nominal_hashrate.gigahash_per_second (already GH/s)
        hashrate_ths = 0
        power_watts = 0
        accepted = 0
        rejected = 0

        if not stats.get("error"):
            miner_stats = stats.get("miner_stats", {})
            # Hashrate in gigahash_per_second (GH/s), convert to TH/s
            nominal = miner_stats.get("nominal_hashrate", {})
            hashrate_ghs = _safe_num(nominal.get("gigahash_per_second", 0))
            if not hashrate_ghs:
                # Fallback to real_hashrate.last_5m
                real = miner_stats.get("real_hashrate", {})
                last5m = real.get("last_5m", {})
                hashrate_ghs = _safe_num(last5m.get("gigahash_per_second", 0))
            hashrate_ths = hashrate_ghs / 1000 if hashrate_ghs else 0

            # Power: power_stats.approximated_consumption.watt
            power_stats = stats.get("power_stats", {})
            consumption = power_stats.get("approximated_consumption", {})
            power_watts = _safe_num(consumption.get("watt", 0))

            # Pool stats
            pool_stats = stats.get("pool_stats", {})
            accepted = _safe_num(pool_stats.get("accepted_shares", 0))
            rejected = _safe_num(pool_stats.get("rejected_shares", 0)) + _safe_num(pool_stats.get("stale_shares", 0))

        # Extract temperatures and fan speeds from cooling
        chip_temp = 0
        board_temp = 0
        fan_speed = 0

        if not cooling.get("error"):
            # BOS+ API v1: highest_temperature.temperature.degree_c
            highest = cooling.get("highest_temperature", {})
            temp_obj = highest.get("temperature", {})
            degree_c = _safe_num(temp_obj.get("degree_c", 0))
            if degree_c and degree_c > 0:
                chip_temp = degree_c
                board_temp = degree_c  # Only highest temp available in this endpoint

            # Fans: fans[].rpm
            fans = cooling.get("fans", [])
            fan_speeds = [_safe_num(f.get("rpm", 0)) for f in fans if f.get("rpm")]
            if fan_speeds:
                avg_rpm = sum(fan_speeds) / len(fan_speeds)
                fan_speed = min(100, int(avg_rpm / 60))  # Rough percentage

        # Extract pool URL
        pool_url = ""
        if not pools.get("error"):
            pool_groups = pools.get("pool_groups", [])
            if pool_groups:
                first_group = pool_groups[0]
                pool_list = first_group.get("pools", [])
                if pool_list:
                    pool_url = pool_list[0].get("url", "")

        # Get model from details
        # BOS+ API v1: miner_identity.name, bos_version.current, bosminer_uptime_s
        model = "BraiinsOS"
        version = ""
        uptime = 0
        if not details.get("error"):
            identity = details.get("miner_identity", {})
            model = identity.get("name", "") or identity.get("miner_model", "") or details.get("hostname", "BraiinsOS")
            bos_ver = details.get("bos_version", {})
            version = bos_ver.get("current", "") or bos_ver.get("major", "")
            uptime = _safe_num(details.get("bosminer_uptime_s", 0)) or _safe_num(details.get("system_uptime_s", 0))

        return {
            "online": True,
            "hashrate_ths": hashrate_ths,
            "hashrate_ghs": hashrate_ths * 1000,  # Also provide GH/s for compatibility
            "power_watts": power_watts,
            "temps": {
                "chip": chip_temp,
                "board": board_temp
            },
            "uptime": uptime,
            "accepted": accepted,
            "rejected": rejected,
            "best_diff": "0",  # BraiinsOS doesn't track this the same way
            "pool_url": pool_url,
            "model": model,
            "version": version,
            "fan_speed": fan_speed,
            "voltage": power_stats.get("input_voltage", {}).get("volt", 0) if isinstance(power_stats, dict) else 0,
            "efficiency": power_watts / hashrate_ths if hashrate_ths > 0 else 0  # J/TH
        }

    except Exception as e:
        return {
            "online": False,
            "error": str(e),
            "hashrate_ths": 0,
            "hashrate_ghs": 0,
            "power_watts": 0,
            "voltage": 0,
            "temps": {"chip": 0, "board": 0},
            "uptime": 0,
            "accepted": 0,
            "rejected": 0,
            "best_diff": "0"
        }


# Vnish session cache for token reuse
_vnish_sessions = {}  # {ip: {"token": str, "expires": timestamp}}
_vnish_sessions_lock = threading.Lock()


def vnish_api_call(ip, endpoint, method="GET", data=None, password="admin", timeout=10):
    """Make authenticated API call to Vnish firmware miner.

    Vnish uses token-based authentication on port 80 (standard HTTP).
    Auth: POST /api/v1/unlock → plain token (not Bearer for data endpoints).
    Also has CGMiner-compatible RPC on port 4028.
    Reference: pyasic vnish backend | https://vnish.group/

    Args:
        ip: Miner IP address
        endpoint: API endpoint (e.g., "/api/v1/summary")
        method: HTTP method
        data: Optional JSON data
        password: Vnish password (default: admin)
        timeout: Request timeout

    Returns:
        dict: API response or {"error": "message"}
    """
    if not validate_miner_ip(ip):
        return {"error": "Invalid or blocked IP address (SSRF protection)"}

    base_url = f"http://{ip}"

    # Check for cached valid token
    now = time.time()
    with _vnish_sessions_lock:
        session = _vnish_sessions.get(ip, {})
        token = session.get("token")
        expires = session.get("expires", 0)

    # Token expired or not present - authenticate
    if not token or now >= expires:
        try:
            auth_response = requests.post(
                f"{base_url}/api/v1/unlock",
                json={"pw": password},
                timeout=timeout
            )
            if auth_response.status_code == 200:
                auth_data = auth_response.json()
                token = auth_data.get("token", "")
                with _vnish_sessions_lock:
                    _vnish_sessions[ip] = {
                        "token": token,
                        "expires": now + 3000  # Refresh before expiry
                    }
            else:
                return {"error": f"Vnish auth failed: {auth_response.status_code}"}
        except requests.exceptions.RequestException as e:
            return {"error": f"Vnish auth error: {str(e)}"}

    # Vnish uses plain token for GET data endpoints, Bearer only for system/* POST commands
    if method == "POST" and endpoint.startswith("/api/v1/system"):
        headers = {"Authorization": f"Bearer {token}"} if token else {}
    else:
        headers = {"Authorization": token} if token else {}

    try:
        if method == "GET":
            response = requests.get(f"{base_url}{endpoint}", headers=headers, timeout=timeout)
        elif method == "POST":
            response = requests.post(f"{base_url}{endpoint}", headers=headers, json=data, timeout=timeout)
        else:
            return {"error": f"Unsupported method: {method}"}

        if response.status_code == 200:
            return response.json()
        elif response.status_code == 401:
            with _vnish_sessions_lock:
                _vnish_sessions.pop(ip, None)
            return {"error": "Vnish auth expired"}
        else:
            return {"error": f"Vnish API returned {response.status_code}"}

    except requests.exceptions.RequestException as e:
        return {"error": str(e)}


def fetch_vnish(ip, password="admin", timeout=10):
    """Fetch data from Vnish firmware miner via REST API on port 80 + CGMiner RPC on 4028.

    Vnish is custom firmware for Antminers (S9, S17, S19, etc.)
    Web API on port 80: /api/v1/unlock, /api/v1/summary, /api/v1/metrics, /api/v1/info
    CGMiner-compatible RPC on port 4028 for hashrate (more reliable).
    API docs: http://[miner]/docs/

    Reference: https://vnish.group/ | pyasic vnish backend
    """
    if not validate_miner_ip(ip):
        return {"online": False, "error": "Invalid IP (SSRF protection)"}

    try:
        # Get summary data from web API (port 80)
        summary = vnish_api_call(ip, "/api/v1/summary", password=password, timeout=timeout)
        if summary.get("error"):
            raise Exception(summary.get("error"))

        # Get info for model/version
        info = vnish_api_call(ip, "/api/v1/info", password=password, timeout=timeout)

        # Extract power from summary.miner.power_usage (verified field path)
        miner_data = summary.get("miner", {})
        power_watts = _safe_num(miner_data.get("power_usage", 0))

        # Fallback: try metrics endpoint for power
        if not power_watts:
            metrics = vnish_api_call(ip, "/api/v1/metrics", password=password, timeout=timeout)
            if not metrics.get("error"):
                power_watts = _safe_num(metrics.get("power_consumption", 0)) or _safe_num(metrics.get("power", 0))

        # Extract hashrate from CGMiner RPC (port 4028) — more reliable than web API
        hashrate_ths = 0
        accepted = 0
        rejected = 0
        uptime = 0
        chip_temp = 0
        board_temp = 0
        fan_speeds = []

        try:
            cgm_summary = cgminer_command(ip, 4028, "summary", timeout=timeout)
            if "SUMMARY" in cgm_summary:
                s = cgm_summary["SUMMARY"][0] if cgm_summary["SUMMARY"] else {}
                ghs = _safe_num(s.get("GHS 5s", 0)) or _safe_num(s.get("GHS av", 0))
                if not ghs:
                    mhs = _safe_num(s.get("MHS 5s", 0)) or _safe_num(s.get("MHS av", 0))
                    ghs = mhs / 1000
                hashrate_ths = ghs / 1000
                accepted = _safe_num(s.get("Accepted", 0))
                rejected = _safe_num(s.get("Rejected", 0))
                uptime = _safe_num(s.get("Elapsed", 0))

            cgm_stats = cgminer_command(ip, 4028, "stats", timeout=timeout)
            if "STATS" in cgm_stats:
                for s in cgm_stats["STATS"]:
                    ct = [_safe_num(s.get(f"temp{i}", 0)) for i in range(1, 4) if _safe_num(s.get(f"temp{i}", 0)) > 0]
                    if ct:
                        chip_temp = max(ct)
                    bt = [_safe_num(s.get(f"temp2_{i}", 0)) for i in range(1, 4) if _safe_num(s.get(f"temp2_{i}", 0)) > 0]
                    if bt:
                        board_temp = max(bt)
                    fans = [_safe_num(s.get(f"fan{i}", 0)) for i in range(1, 5) if _safe_num(s.get(f"fan{i}", 0)) > 0]
                    if fans:
                        fan_speeds = fans
        except Exception:
            pass

        # Fallback: web API hr_nominal if CGMiner didn't work
        if not hashrate_ths:
            hr = summary.get("hr_nominal", 0)
            if hr:
                hashrate_ths = _safe_num(hr)

        # Extract model/version from info endpoint
        model = "Vnish"
        version = ""
        if not info.get("error"):
            model = info.get("build_name", "Vnish")
            version = info.get("build_uuid", "")[:8] if info.get("build_uuid") else ""
        # Also try miner_type from summary
        if model == "Vnish" and miner_data.get("miner_type"):
            model = miner_data["miner_type"]

        # Fan speed as percentage for display
        fan_speed = 0
        if fan_speeds:
            avg_rpm = sum(fan_speeds) / len(fan_speeds)
            fan_speed = min(100, int(avg_rpm / 60))

        return {
            "online": True,
            "hashrate_ths": hashrate_ths,
            "hashrate_ghs": hashrate_ths * 1000,
            "power_watts": power_watts,
            "temps": {"chip": chip_temp, "board": board_temp},
            "uptime": uptime,
            "accepted": accepted,
            "rejected": rejected,
            "best_diff": "0",
            "model": model,
            "version": version,
            "fan_speed": fan_speed,
            "voltage": 0,  # Vnish API doesn't expose voltage directly
            "efficiency": power_watts / hashrate_ths if hashrate_ths > 0 else 0
        }

    except Exception as e:
        return {
            "online": False, "error": str(e),
            "hashrate_ths": 0, "hashrate_ghs": 0, "power_watts": 0, "voltage": 0,
            "temps": {"chip": 0, "board": 0}, "uptime": 0,
            "accepted": 0, "rejected": 0, "best_diff": "0"
        }


def luxos_command(ip, port, command, parameter=None, timeout=5):
    """Send command to LuxOS miner via TCP socket.

    LuxOS uses a CGMiner-compatible API on port 4028.
    Similar to cgminer_command but with LuxOS-specific handling.

    Reference: https://docs.luxor.tech/firmware/api/intro
    """
    if not validate_miner_ip(ip):
        return {"error": "Invalid or blocked IP address (SSRF protection)"}

    sock = None
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(timeout)
        sock.connect((ip, port))

        payload = {"command": command}
        if parameter is not None:
            payload["parameter"] = str(parameter)

        sock.sendall(json.dumps(payload).encode())

        response = b""
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            response += chunk
            if b'\x00' in chunk:
                break

        response_str = response.decode('utf-8', errors='ignore').replace('\x00', '')

        # LuxOS response format
        if '|' in response_str:
            response_str = response_str.split('|', 1)[1]
        if response_str.endswith('EOF'):
            response_str = response_str[:-3]

        return json.loads(response_str)

    except (socket.error, socket.timeout, OSError, json.JSONDecodeError) as e:
        return {"error": str(e)}
    finally:
        if sock:
            try:
                sock.close()
            except OSError:
                pass


def fetch_luxos(ip, port=4028, timeout=5):
    """Fetch data from LuxOS firmware miner.

    LuxOS is custom firmware for Antminers.
    Uses CGMiner-compatible TCP API on port 4028.

    Reference: https://docs.luxor.tech/firmware/api/intro
    """
    if not validate_miner_ip(ip):
        return {"online": False, "error": "Invalid IP (SSRF protection)"}

    try:
        # Get summary
        summary = luxos_command(ip, port, "summary", timeout=timeout)
        if summary.get("error"):
            raise Exception(summary.get("error"))

        # Get temps
        temps_data = luxos_command(ip, port, "temps", timeout=timeout)

        # Get fans
        fans_data = luxos_command(ip, port, "fans", timeout=timeout)

        # Get pools
        pools = luxos_command(ip, port, "pools", timeout=timeout)

        summary_data = summary.get("SUMMARY", [{}])[0]

        # Extract hashrate - try GHS first (newer firmware), then MHS (standard CGMiner)
        ghs = _safe_num(summary_data.get('GHS av', 0)) or _safe_num(summary_data.get('GHS 5s', 0))
        if not ghs:
            mhs = _safe_num(summary_data.get('MHS av', 0)) or _safe_num(summary_data.get('MHS 5s', 0))
            ghs = mhs / 1000  # Convert MH/s to GH/s
        hashrate_ths = ghs / 1000  # Convert GH/s to TH/s

        # Power consumption
        power_watts = _safe_num(summary_data.get('Power', 0)) or _safe_num(summary_data.get('power', 0))

        # Extract temps
        chip_temp = 0
        board_temp = 0
        if not temps_data.get("error"):
            temps_list = temps_data.get("TEMPS", [])
            if temps_list:
                chip_temps = [_safe_num(t.get("Chip", 0)) for t in temps_list if t.get("Chip")]
                board_temps = [_safe_num(t.get("Board", 0)) for t in temps_list if t.get("Board")]
                chip_temp = max(chip_temps) if chip_temps else 0
                board_temp = max(board_temps) if board_temps else chip_temp

        # Extract fan speed
        fan_speed = 0
        if not fans_data.get("error"):
            fans_list = fans_data.get("FANS", [])
            if fans_list:
                speeds = [_safe_num(f.get("RPM", 0)) for f in fans_list if f.get("RPM")]
                if speeds:
                    fan_speed = min(100, int(sum(speeds) / len(speeds) / 60))

        # Pool URL
        pool_url = ""
        if not pools.get("error"):
            pools_list = pools.get("POOLS", [])
            if pools_list:
                pool_url = pools_list[0].get("URL", "")

        return {
            "online": True,
            "hashrate_ths": hashrate_ths,
            "hashrate_ghs": hashrate_ths * 1000,
            "power_watts": power_watts,
            "temps": {"chip": chip_temp, "board": board_temp},
            "uptime": _safe_num(summary_data.get("Elapsed", 0)),
            "accepted": _safe_num(summary_data.get("Accepted", 0)),
            "rejected": _safe_num(summary_data.get("Rejected", 0)),
            "best_diff": str(summary_data.get("Best Share", 0)),
            "pool_url": pool_url,
            "model": "LuxOS",
            "fan_speed": fan_speed,
            "voltage": _safe_num(summary_data.get("Voltage", 0)),
            "efficiency": power_watts / hashrate_ths if hashrate_ths > 0 else 0
        }

    except Exception as e:
        return {
            "online": False, "error": str(e),
            "hashrate_ths": 0, "hashrate_ghs": 0, "power_watts": 0,
            "temps": {"chip": 0, "board": 0}, "uptime": 0,
            "accepted": 0, "rejected": 0, "best_diff": "0"
        }


def fetch_epic_http(ip, port=4028, username="root", password="letmein", timeout=10):
    """Fetch data from ePIC BlockMiner via HTTP REST API.

    IMPORTANT: ePIC uses HTTP REST on port 4028, NOT CGMiner TCP socket.
    Endpoints: GET /summary, /hashrate, /fanspeed, /capabilities
    Default credentials: root / letmein
    Reference: https://github.com/epicblockchain/epic-miner | pyasic epic backend
    """
    if not validate_miner_ip(ip):
        return {"online": False, "error": "Invalid IP (SSRF protection)"}

    try:
        base_url = f"http://{ip}:{port}"
        auth = (username, password)

        # Fetch summary (main stats endpoint)
        summary = requests.get(f"{base_url}/summary", auth=auth, timeout=timeout)
        if summary.status_code != 200:
            raise Exception(f"ePIC API returned {summary.status_code}")
        summary_data = summary.json()

        # Extract hashrate
        mining = summary_data.get("Mining", {})
        ghs = _safe_num(mining.get("Speed(GHS)", 0)) or _safe_num(mining.get("GHS av", 0))
        hashrate_ths = ghs / 1000
        accepted = _safe_num(mining.get("Accepted", 0))
        rejected = _safe_num(mining.get("Rejected", 0))

        # Uptime
        session = summary_data.get("Session", {})
        uptime = _safe_num(session.get("Uptime", 0)) or _safe_num(session.get("Elapsed", 0))

        # Stratum user
        stratum = summary_data.get("Stratum", {})
        pool_user = stratum.get("Current User", "")
        pool_url = stratum.get("Current Pool", "")

        # Hashboard temps
        chip_temp = 0
        board_temp = 0
        hbs = summary_data.get("HBs", [])
        if isinstance(hbs, list):
            chip_temps = []
            for hb in hbs:
                temp = _safe_num(hb.get("Temperature", 0)) or _safe_num(hb.get("Chip Temp", 0))
                if temp > 0:
                    chip_temps.append(temp)
            if chip_temps:
                chip_temp = max(chip_temps)
                board_temp = min(chip_temps) if len(chip_temps) > 1 else chip_temp

        # Fan speeds
        fan_speed = 0
        try:
            fan_resp = requests.get(f"{base_url}/fanspeed", auth=auth, timeout=timeout)
            if fan_resp.status_code == 200:
                fan_data = fan_resp.json()
                fans = fan_data.get("Fans", [])
                if isinstance(fans, list):
                    rpms = [_safe_num(f.get("RPM", 0)) for f in fans if _safe_num(f.get("RPM", 0)) > 0]
                    if rpms:
                        fan_speed = min(100, int(sum(rpms) / len(rpms) / 60))
        except Exception:
            pass

        # Power
        power_watts = 0
        try:
            cap_resp = requests.get(f"{base_url}/capabilities", auth=auth, timeout=timeout)
            if cap_resp.status_code == 200:
                caps = cap_resp.json()
                power_watts = _safe_num(caps.get("Power Consumption", 0)) or _safe_num(caps.get("Power", 0))
        except Exception:
            pass

        return {
            "online": True,
            "hashrate_ths": hashrate_ths,
            "hashrate_ghs": ghs,
            "power_watts": power_watts,
            "temps": {"chip": chip_temp, "board": board_temp},
            "uptime": uptime,
            "accepted": accepted,
            "rejected": rejected,
            "best_diff": "0",
            "pool_url": pool_url,
            "pool_user": pool_user,
            "model": "ePIC BlockMiner",
            "fan_speed": fan_speed,
            "efficiency": power_watts / hashrate_ths if hashrate_ths > 0 else 0
        }

    except Exception as e:
        return {
            "online": False, "error": str(e),
            "hashrate_ths": 0, "hashrate_ghs": 0, "power_watts": 0,
            "temps": {"chip": 0, "board": 0}, "uptime": 0,
            "accepted": 0, "rejected": 0, "best_diff": "0"
        }


def fetch_axeos(ip, timeout=5):
    """Fetch data from AxeOS/NMAXE device via HTTP API"""
    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return {
            "online": False,
            "error": "Invalid or blocked IP address (SSRF protection)"
        }
    try:
        url = f"http://{ip}/api/system/info"
        response = requests.get(url, timeout=timeout)
        response.raise_for_status()
        data = response.json()

        # NMAxe firmware (v2.9.x+) uses a completely different field schema from standard AxeOS.
        # Detect by: hwModel == "NMAxe", OR has NMAxe-specific fields (asicTemp, fans array).
        # NOTE: Cannot use isinstance(stratum, dict) alone — NerdQAxe++ v1.0.36+ also has a
        # nested stratum object but uses standard AxeOS field names (temp, not asicTemp).
        is_nmaxe = (
            str(data.get('hwModel', '')).upper() == 'NMAXE' or
            (isinstance(data.get('stratum'), dict) and 'asicTemp' in data)
        )

        if is_nmaxe:
            stratum = data.get('stratum', {})
            pool_url = stratum.get('used', {}).get('url', '')
            fans_list = data.get('fans', [])
            fan_rpm = _safe_num(fans_list[0].get('rpm', 0)) if fans_list else 0
            return {
                "online": True,
                "hashrate_ghs": _safe_num(data.get('hashRate', 0)),
                "power_watts": _safe_num(data.get('power', 0)),
                "temps": {
                    "chip": _safe_num(data.get('asicTemp', 0)),   # ASIC die temp
                    "board": _safe_num(data.get('mcuTemp', 0)),   # MCU/board temp
                    "vr": _safe_num(data.get('vcoreTemp', 0))     # VCore regulator temp
                },
                "uptime": _safe_num(data.get('uptimeSeconds', 0)),
                "accepted": _safe_num(data.get('sharesAccepted', 0)),
                "rejected": _safe_num(data.get('sharesRejected', 0)),
                "best_diff": data.get('bestDiffEver', '0'),
                "pool_url": pool_url,
                "hostname": data.get('hostName', ip),          # capital N in NMAxe API
                "version": data.get('fwVersion', 'Unknown'),   # NMAxe uses fwVersion not version
                "fan_speed": fan_rpm,                          # fans[0].rpm
                "frequency": _safe_num(data.get('freqReq', 0)),           # NMAxe uses freqReq not frequency
                "voltage": _safe_num(data.get('vcoreActual', 0)) or _safe_num(data.get('vcoreReq', 0))  # core voltage in mV
            }

        # Standard AxeOS format (BitAxe, NerdQAxe, QAxe, etc.)
        # hashRate reported in GH/s directly (e.g., 5051.922 = 5051 GH/s = 5.05 TH/s)
        hashrate_ghs = _safe_num(data.get('hashRate', 0))

        return {
            "online": True,
            "hashrate_ghs": hashrate_ghs,
            "power_watts": _safe_num(data.get('power', 0)),
            "temps": {
                "chip": _safe_num(data.get('temp', 0)),
                "board": _safe_num(data.get('temp2', 0)) or _safe_num(data.get('vrTemp', 0)),  # temp2 is secondary sensor per API docs
                "vr": _safe_num(data.get('vrTemp', 0))
            },
            "uptime": _safe_num(data.get('uptimeSeconds', 0)),
            "accepted": _safe_num(data.get('sharesAccepted', 0)),
            "rejected": _safe_num(data.get('sharesRejected', 0)),
            "best_diff": data.get('bestDiff', '0'),
            "pool_url": f"{data.get('stratumURL', '')}:{data['stratumPort']}" if data.get('stratumPort') is not None and str(data.get('stratumPort', '')).strip() else data.get('stratumURL', ''),
            "hostname": data.get('hostname', ip),
            "version": data.get('version', 'Unknown'),
            "fan_speed": _safe_num(data.get('fanspeed', 0)),  # API uses lowercase 'fanspeed' only
            "frequency": _safe_num(data.get('frequency', 0)),
            "voltage": _safe_num(data.get('coreVoltage', 0)) or _safe_num(data.get('voltage', 0))
        }
    except Exception as e:
        return {
            "online": False,
            "error": str(e),
            "hashrate_ghs": 0,
            "power_watts": 0,
            "temps": {"chip": 0, "board": 0, "vr": 0},
            "uptime": 0,
            "accepted": 0,
            "rejected": 0,
            "best_diff": "0"
        }


def fetch_esp32miner(ip, timeout=5):
    """DEPRECATED: ESP32 Miner has NO HTTP API.

    This function is kept for potential future ESP32 Miner variants that might expose
    an HTTP API, but standard ESP32 Miner (ESP32-only) does NOT have any HTTP server.

    For ESP32 Miner, the main miner fetch loop now polls the pool's stratum
    connections API via fetch_pool_worker_stats() instead of calling this function.
    """
    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return {
            "online": False,
            "error": "Invalid or blocked IP address (SSRF protection)"
        }

    # Try ESP32 Miner-specific endpoints
    endpoints = ["/api/status", "/status", "/api/system/info"]

    for endpoint in endpoints:
        try:
            url = f"http://{ip}{endpoint}"
            response = requests.get(url, timeout=timeout)
            response.raise_for_status()
            data = response.json()

            # ESP32 Miner reports hashrate in H/s (very low - kH/s range)
            hashrate_hs = _safe_num(data.get("hashRate", data.get("hashrate", 0)))

            # Convert to GH/s - ESP32 Miner hashrate is typically in H/s
            if hashrate_hs > 1000000:  # Likely in H/s
                hashrate_ghs = hashrate_hs / 1e9
            elif hashrate_hs > 1000:  # Likely in kH/s
                hashrate_ghs = hashrate_hs / 1e6
            else:  # Could be very low or already converted
                hashrate_ghs = hashrate_hs / 1e9 if hashrate_hs > 100 else hashrate_hs

            # ESP32 Miner uses 'valid'/'invalid' for shares
            accepted = _safe_num(data.get("valid", data.get("sharesAccepted", data.get("valids", 0))))
            rejected = _safe_num(data.get("invalid", data.get("sharesRejected", data.get("invalids", 0))))


            return {
                "online": True,
                "hashrate_ghs": hashrate_ghs,
                "power_watts": _safe_num(data.get("power", 2)),  # Default 2W for ESP32
                "temps": {
                    "chip": _safe_num(data.get("temp", 0)),
                    "board": _safe_num(data.get("boardTemp", 0))
                },
                "uptime": _safe_num(data.get("uptimeSeconds", data.get("uptime", data.get("elapsed", 0)))),
                "accepted": accepted,
                "rejected": rejected,
                "best_diff": data.get("bestDiff", data.get("best_diff", data.get("bestDifficulty", "0"))),
                "pool_url": data.get("stratumURL", data.get("pool", "")),
                "hostname": data.get("hostname", data.get("name", "ESP32 Miner")),
                "version": data.get("version", "Unknown")
            }
        except Exception:
            continue

    # All endpoints failed
    return {
        "online": False,
        "error": "ESP32 Miner not responding",
        "hashrate_ghs": 0,
        "power_watts": 0,
        "temps": {"chip": 0, "board": 0},
        "uptime": 0,
        "accepted": 0,
        "rejected": 0,
        "best_diff": "0"
    }


def _safe_num(val, default=0):
    """Convert a value to a number, handling strings from CGMiner API responses."""
    if isinstance(val, (int, float)):
        return val
    try:
        return float(val)
    except (TypeError, ValueError):
        return default


def cgminer_command(ip, port, command, parameter=None, timeout=5):
    """Send command to CGMiner API.

    Args:
        ip: Miner IP address
        port: CGMiner API port (usually 4028)
        command: CGMiner command (e.g., 'summary', 'pools', 'addpool', 'switchpool')
        parameter: Optional parameter for commands that need it (e.g., pool URL for addpool)
        timeout: Socket timeout in seconds

    CGMiner API commands with parameters:
        - addpool|URL,worker,password - Add a new pool
        - switchpool|N - Switch to pool N (0-indexed)
        - enablepool|N - Enable pool N
        - disablepool|N - Disable pool N
        - removepool|N - Remove pool N
        - poolpriority|N,N,N... - Set pool priority order
    """
    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return {"error": "Invalid or blocked IP address (SSRF protection)"}
    sock = None
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(timeout)
        sock.connect((ip, port))

        # Build command payload - CGMiner uses {"command": "cmd", "parameter": "param"} format
        payload = {"command": command}
        if parameter is not None:
            payload["parameter"] = str(parameter)

        sock.sendall(json.dumps(payload).encode())

        response = b""
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            response += chunk
            if b'\x00' in chunk:
                break

        # Parse response - CGMiner sends |{JSON}EOF format
        response_str = response.decode('utf-8', errors='ignore')
        response_str = response_str.replace('\x00', '')

        # Find JSON in response
        if '|' in response_str:
            response_str = response_str.split('|', 1)[1]
        if response_str.endswith('EOF'):
            response_str = response_str[:-3]

        return json.loads(response_str)
    except (socket.error, socket.timeout, OSError, json.JSONDecodeError) as e:
        return {"error": str(e)}
    finally:
        if sock:
            try:
                sock.close()
            except OSError:
                pass


def fetch_avalon(ip, port=4028, timeout=5):
    """Fetch data from Avalon device via CGMiner API.

    Supported models: Avalon 1066, 1126, 1166, 1246, 1346, etc.

    CGMiner API Commands used:
    - summary: Overall mining stats (hashrate, accepted/rejected shares)
    - stats: Detailed hardware stats (temperatures, fan speeds, frequency)
    - pools: Pool connection information

    Reference: https://canaan.io/product
    """
    # SECURITY: IP validation is handled in cgminer_command
    try:
        # Get all data from CGMiner API
        summary = cgminer_command(ip, port, "summary", timeout)
        stats = cgminer_command(ip, port, "stats", timeout)
        pools = cgminer_command(ip, port, "pools", timeout)

        if "error" in summary:
            raise Exception(summary["error"])

        summary_data = summary.get("SUMMARY", [{}])[0]

        # Extract hashrate - try GHS first (newer firmware), then MHS (standard CGMiner)
        ghs = _safe_num(summary_data.get('GHS av', 0)) or _safe_num(summary_data.get('GHS 5s', 0))
        if not ghs:
            mhs = _safe_num(summary_data.get('MHS av', 0)) or _safe_num(summary_data.get('MHS 5s', 0))
            ghs = mhs / 1000  # Convert MH/s to GH/s
        hashrate_ghs = ghs

        # Extract temperatures, fan speed, frequency from stats
        chip_temp = 0
        board_temp = 0
        fan_speed = 0
        frequency = 0
        voltage = 0
        model = "Avalon"
        power_watts = 0

        if "STATS" in stats:
            for stat in stats["STATS"]:
                # Parse Avalon Nano 3 / Q series "MM ID0" string which contains all the data
                # Format: "Ver[Nano3s-...] ... TMax[93] TAvg[89] ... Fan1[1440] FanR[31%] ... GHSavg[6013.19] Freq[465.54] ..."
                mm_id0 = stat.get("MM ID0", "")
                if mm_id0 and isinstance(mm_id0, str):
                    import re
                    # Extract model from Ver[...]
                    # Keep model names short to avoid breaking miner card layout
                    ver_match = re.search(r'Ver\[([^\]]+)\]', mm_id0)
                    if ver_match and model == "Avalon":
                        ver_str = ver_match.group(1)
                        # Simplify model names for cleaner display
                        if "Nano" in ver_str:
                            model = "Avalon"  # Nano 3S, Nano, etc. -> just "Avalon"
                        elif "Q0" in ver_str or ver_str.startswith("Q"):
                            model = "Avalon Q"  # Q004, Q003, etc. -> "Avalon Q"
                        else:
                            # For other models, extract just the series (e.g., "1246" from "AV1246-...")
                            series_match = re.search(r'(\d{4})', ver_str)
                            if series_match:
                                model = f"Avalon {series_match.group(1)}"
                            else:
                                model = "Avalon"  # Fallback to generic

                    # Extract TAvg (average chip temp) - more representative than TMax
                    tavg_match = re.search(r'TAvg\[(\d+)\]', mm_id0)
                    if tavg_match and chip_temp == 0:
                        chip_temp = int(tavg_match.group(1))
                    # Fallback to TMax if TAvg not available
                    if chip_temp == 0:
                        tmax_match = re.search(r'TMax\[(\d+)\]', mm_id0)
                        if tmax_match:
                            chip_temp = int(tmax_match.group(1))

                    # Extract OTemp (outside/exhaust temp)
                    otemp_match = re.search(r'OTemp\[(-?\d+)\]', mm_id0)
                    if otemp_match and board_temp == 0:
                        board_temp = int(otemp_match.group(1))

                    # Extract Fan1 (RPM) or FanR (percentage)
                    fan_match = re.search(r'Fan1\[(\d+)\]', mm_id0)
                    if fan_match and fan_speed == 0:
                        fan_speed = int(fan_match.group(1))

                    # Extract Freq
                    freq_match = re.search(r'Freq\[([0-9.]+)\]', mm_id0)
                    if freq_match and frequency == 0:
                        frequency = float(freq_match.group(1))

                    # Extract PS (Power Supply info) - format: PS[0 0 27447 4 0 3626 132]
                    # Values: [0]=?, [1]=?, [2]=voltage_mV, [3]=?, [4]=?, [5]=?, [6]=power_watts
                    # The 7th value (132) is the actual power consumption in watts
                    ps_match = re.search(r'PS\[([^\]]+)\]', mm_id0)
                    if ps_match:
                        ps_values = ps_match.group(1).split()
                        if len(ps_values) >= 7:
                            try:
                                # Voltage in mV (convert to V)
                                if voltage == 0 and int(ps_values[2]) > 0:
                                    voltage = int(ps_values[2]) / 1000
                                # Power in watts (7th value is actual power consumption)
                                if power_watts == 0 and int(ps_values[6]) > 0:
                                    power_watts = int(ps_values[6])
                            except (ValueError, IndexError):
                                pass

                # Get model info - prefer Type/Description over ID
                # ID often contains "pool0", "pool1" etc. which is not the device model
                # NOTE: Only update model if we haven't already extracted it from Ver[]
                if model == "Avalon":
                    raw_model = None
                    # Try to get actual model name from various fields
                    if stat.get("Type"):
                        raw_model = stat.get("Type")
                    elif stat.get("Description"):
                        raw_model = stat.get("Description")
                    elif stat.get("STATS") and isinstance(stat.get("STATS"), str):
                        raw_model = stat.get("STATS")
                    elif stat.get("ID") and not stat.get("ID", "").startswith("pool"):
                        raw_model = stat.get("ID")

                    # Sanitize model name to keep it short for dashboard display
                    if raw_model:
                        raw_lower = raw_model.lower()
                        if "nano" in raw_lower:
                            model = "Avalon"
                        elif "q0" in raw_lower or raw_lower.startswith("q"):
                            model = "Avalon Q"
                        else:
                            # Extract 4-digit series number if present
                            series_match = re.search(r'(\d{4})', raw_model)
                            if series_match:
                                model = f"Avalon {series_match.group(1)}"
                            elif len(raw_model) <= 15:
                                # Only use raw if it's reasonably short
                                model = raw_model
                            else:
                                model = "Avalon"

                # Temperature fields vary by model
                # Try common field names first
                if chip_temp == 0:
                    chip_temp = _safe_num(stat.get("temp", stat.get("temp1", stat.get("TEMP", 0))))
                if board_temp == 0:
                    board_temp = _safe_num(stat.get("temp2", stat.get("temp_pcb", 0)))

                # Fan speed (percentage or RPM)
                if fan_speed == 0:
                    fan_speed = _safe_num(stat.get("fan1", stat.get("Fan Speed In", stat.get("fan", 0))))

                # Frequency in MHz
                if frequency == 0:
                    frequency = _safe_num(stat.get("frequency", stat.get("Frequency", 0)))

                # Voltage
                if voltage == 0:
                    voltage = _safe_num(stat.get("voltage", stat.get("Voltage", 0)))

                # Avalon Q series specific fields (Q004, Q003, etc.)
                # These miners report temps in format like "TMax[85] TAvg[82]" or individual chip temps
                if chip_temp == 0:
                    # Try TMax (maximum chip temperature)
                    if "TMax" in stat:
                        chip_temp = stat.get("TMax", 0)
                    # Try parsing from string format "TMax[85]"
                    for key, value in stat.items():
                        if isinstance(value, str) and "TMax[" in value:
                            try:
                                import re
                                match = re.search(r'TMax\[(\d+)\]', value)
                                if match:
                                    chip_temp = int(match.group(1))
                                    break
                            except (ValueError, AttributeError):
                                pass

                # Avalon Q series exhaust/intake temps
                if board_temp == 0:
                    # Exhaust temp often reported separately
                    board_temp = stat.get("Exhaust", stat.get("exhaust", stat.get("TEnv", 0)))

                # Avalon Q series fan speed (usually in RPM)
                if fan_speed == 0:
                    fan_speed = stat.get("Fan In", stat.get("Fan Out", stat.get("FanR", 0)))

                # Avalon-specific fields (older Avalon nano controller format)
                if "MM Count" in stat:
                    # This is an Avalon controller - extract temps from MM modules
                    mm_count = stat.get("MM Count", 0)
                    if mm_count > 0:
                        # Try to get temp from first MM module
                        for i in range(1, mm_count + 1):
                            temp_key = f"MM ID{i} Temp"
                            if temp_key in stat and chip_temp == 0:
                                chip_temp = stat.get(temp_key, 0)

                # Avalon Q series - parse SYSTEMSTATU or similar compound fields
                if chip_temp == 0 and "SYSTEMSTATU" in stat:
                    # Some Avalons pack multiple values in SYSTEMSTATU
                    sys_status = stat.get("SYSTEMSTATU", "")
                    if isinstance(sys_status, str):
                        try:
                            import re
                            temp_match = re.search(r'Temp\[(\d+)\]|TMax\[(\d+)\]', sys_status)
                            if temp_match:
                                chip_temp = int(temp_match.group(1) or temp_match.group(2))
                        except (ValueError, AttributeError):
                            pass

        # Extract pool info
        pool_url = ""
        pool_user = ""
        pool_status = ""
        if "POOLS" in pools and pools["POOLS"]:
            # Find the active pool (Status = Alive and Stratum Active = true)
            for pool in pools["POOLS"]:
                if pool.get("Status") == "Alive" and pool.get("Stratum Active", False):
                    pool_url = pool.get("URL", "")
                    pool_user = pool.get("User", "")
                    pool_status = "connected"
                    break
            # Fallback to first pool if no active one found
            if not pool_url and pools["POOLS"]:
                first_pool = pools["POOLS"][0]
                pool_url = first_pool.get("URL", "")
                pool_user = first_pool.get("User", "")
                pool_status = first_pool.get("Status", "unknown")

        # Use actual power if extracted from API, otherwise estimate
        if power_watts == 0:
            # Estimate power based on known Avalon models
            model_lower = model.lower()
            if 'nano' in model_lower:
                if '3' in model_lower:
                    power_watts = 140  # Avalon Nano 3 (~6.5 TH/s)
                else:
                    power_watts = 120  # Older Nano models
            elif hashrate_ghs > 0:
                # Estimate ~20 J/TH for modern Avalon ASICs
                power_watts = int((hashrate_ghs / 1000) * 20)

        return {
            "online": True,
            "model": model,
            "hashrate_ghs": hashrate_ghs,
            "hashrate_ths": hashrate_ghs / 1000,
            "power_watts": power_watts,
            "temps": {
                "chip": chip_temp,
                "board": board_temp
            },
            "fan_speed": fan_speed,
            "frequency": frequency,
            "voltage": voltage,
            "uptime": _safe_num(summary_data.get('Elapsed', 0)),
            "accepted": _safe_num(summary_data.get('Accepted', 0)),
            "rejected": _safe_num(summary_data.get('Rejected', 0)),
            "stale": _safe_num(summary_data.get('Stale', 0)),
            "best_diff": str(summary_data.get('Best Share', 0)),
            "difficulty": _safe_num(summary_data.get('Difficulty Accepted', 0)),
            "pool_url": pool_url,
            "pool_user": pool_user,
            "pool_status": pool_status,
            "hardware_errors": _safe_num(summary_data.get('Hardware Errors', 0)),
            "getworks": _safe_num(summary_data.get('Getworks', 0)),
            "work_utility": _safe_num(summary_data.get('Work Utility', 0))
        }
    except Exception as e:
        return {
            "online": False,
            "error": str(e),
            "model": "Avalon",
            "hashrate_ghs": 0,
            "hashrate_ths": 0,
            "power_watts": 0,
            "temps": {"chip": 0, "board": 0},
            "fan_speed": 0,
            "frequency": 0,
            "voltage": 0,
            "uptime": 0,
            "accepted": 0,
            "rejected": 0,
            "stale": 0,
            "best_diff": "0",
            "pool_url": "",
            "pool_user": "",
            "pool_status": "offline"
        }


def fetch_antminer(ip, port=4028, timeout=5):
    """
    Fetch data from Bitmain Antminer device via CGMiner API.

    Supported models: S19, S19 Pro, S19j Pro, S19 XP, S21, T21, etc.

    The Antminer uses a modified CGMiner API with additional stats.
    Reference: https://github.com/bitmaintech/cgminer

    API Commands used:
    - summary: Overall mining stats (hashrate, accepted/rejected shares)
    - stats: Detailed hardware stats (temperatures, fan speeds, chain info)
    - pools: Pool connection information
    """
    # SECURITY: IP validation is handled in cgminer_command
    try:
        # Get summary for hashrate and share counts
        summary = cgminer_command(ip, port, "summary", timeout)
        # Get stats for temperatures, fans, and hardware info
        stats = cgminer_command(ip, port, "stats", timeout)
        # Get pools for pool connection info
        pools = cgminer_command(ip, port, "pools", timeout)

        if "error" in summary:
            raise Exception(summary["error"])

        summary_data = summary.get("SUMMARY", [{}])[0]

        # Extract hashrate - Antminer reports in GH/s or TH/s depending on model
        # Try GHS first (newer models), then MHS (older models)
        ghs = summary_data.get('GHS av', summary_data.get('GHS 5s', 0))
        if not ghs:
            mhs = summary_data.get('MHS av', summary_data.get('MHS 5s', 0))
            ghs = float(mhs or 0) / 1000  # Convert MH/s to GH/s

        hashrate_ghs = float(ghs) if ghs else 0
        hashrate_ths = hashrate_ghs / 1000

        # Extract detailed stats (temperatures, fans, chain status)
        chip_temps = []
        board_temps = []
        inlet_temp = 0
        outlet_temp = 0
        fan_speeds = []
        power_watts = 0
        voltage = 0
        chain_status = []
        model = "Antminer"
        firmware = ""

        if "STATS" in stats:
            for stat in stats["STATS"]:
                # Get model info
                if "Type" in stat:
                    model = stat.get("Type", "Antminer")
                if "miner_version" in stat:
                    firmware = stat.get("miner_version", "")

                # Temperature extraction - Antminer has multiple temp sensors
                # temp1, temp2, temp3 = chip temps for each hashboard
                # temp2_1, temp2_2, temp2_3 = PCB temps for each hashboard
                # NOTE: Stock Antminer CGMiner API may return values as strings
                for i in range(1, 4):
                    chip_temp = _safe_num(stat.get(f"temp{i}", 0))
                    if chip_temp > 0:
                        chip_temps.append(chip_temp)
                    pcb_temp = _safe_num(stat.get(f"temp2_{i}", 0))
                    if pcb_temp > 0:
                        board_temps.append(pcb_temp)

                # Also check for temp_chip and temp_pcb arrays (newer firmware)
                for i in range(1, 4):
                    chip_temp = _safe_num(stat.get(f"temp_chip{i}", 0))
                    if chip_temp > 0 and chip_temp not in chip_temps:
                        chip_temps.append(chip_temp)

                # Inlet/outlet temps (environmental)
                inlet_temp = _safe_num(stat.get("temp_inlet", stat.get("env_temp", 0)))
                outlet_temp = _safe_num(stat.get("temp_outlet", 0))

                # Fan speeds (RPM)
                for i in range(1, 5):
                    fan = _safe_num(stat.get(f"fan{i}", 0))
                    if fan > 0:
                        fan_speeds.append(fan)

                # Power consumption (newer firmware reports this)
                power_watts = _safe_num(stat.get("Power", stat.get("power", 0)))

                # Voltage (mV or V depending on model/firmware)
                if voltage == 0:
                    v = _safe_num(stat.get("voltage", stat.get("Voltage", stat.get("psu_voltage", 0))))
                    if v > 0:
                        voltage = v

                # Chain/hashboard status
                for i in range(1, 4):
                    chain_rate = stat.get(f"chain_rate{i}", 0)
                    chain_hw = stat.get(f"chain_hw{i}", 0)
                    if chain_rate is not None or chain_hw is not None:
                        chain_status.append({
                            "id": i,
                            "hashrate": chain_rate,
                            "hw_errors": chain_hw
                        })

        # Get pool info
        pool_url = ""
        if "POOLS" in pools:
            for pool in pools["POOLS"]:
                if pool.get("Stratum Active"):
                    pool_url = pool.get("URL", "")
                    break

        # Calculate max temps for display
        max_chip_temp = max(chip_temps) if chip_temps else 0
        max_board_temp = max(board_temps) if board_temps else 0
        avg_fan_speed = sum(fan_speeds) // len(fan_speeds) if fan_speeds else 0

        return {
            "online": True,
            "hashrate_ghs": hashrate_ghs,
            "hashrate_ths": hashrate_ths,
            "power_watts": power_watts,
            "temps": {
                "chip": max_chip_temp,
                "board": max_board_temp,
                "inlet": inlet_temp,
                "outlet": outlet_temp,
                "chip_temps": chip_temps,
                "board_temps": board_temps
            },
            "fans": {
                "speeds": fan_speeds,
                "avg_rpm": avg_fan_speed
            },
            "uptime": summary_data.get('Elapsed', 0),
            "accepted": summary_data.get('Accepted', 0),
            "rejected": summary_data.get('Rejected', 0),
            "stale": summary_data.get('Stale', 0),
            "best_diff": str(summary_data.get('Best Share', 0)),
            "difficulty": summary_data.get('Difficulty Accepted', 0),
            "hardware_errors": summary_data.get('Hardware Errors', 0),
            "pool_url": pool_url,
            "model": model,
            "firmware": firmware,
            "voltage": voltage,
            "chains": chain_status
        }
    except Exception as e:
        return {
            "online": False,
            "error": str(e),
            "hashrate_ghs": 0,
            "hashrate_ths": 0,
            "power_watts": 0,
            "temps": {"chip": 0, "board": 0, "inlet": 0, "outlet": 0},
            "fans": {"speeds": [], "avg_rpm": 0},
            "voltage": 0,
            "uptime": 0,
            "accepted": 0,
            "rejected": 0,
            "stale": 0,
            "best_diff": "0",
            "hardware_errors": 0,
            "model": "Antminer",
            "chains": []
        }


def fetch_whatsminer(ip, port=4028, timeout=5):
    """
    Fetch data from MicroBT Whatsminer device via CGMiner API.

    Supported models: M30S, M30S+, M30S++, M50, M50S, M60, M60S, etc.

    The Whatsminer uses CGMiner API (must be enabled in web interface).
    Some models may require enabling "API access" in settings.

    Reference: https://www.whatsminer.com/file/WhatsminerAPI%20V2.0.3.pdf
    GitHub reference: https://github.com/satoshi-anonymoto/whatsminer-api
    """
    # SECURITY: IP validation is handled in cgminer_command
    try:
        # Get summary for hashrate and share counts
        summary = cgminer_command(ip, port, "summary", timeout)
        # Get stats for detailed hardware info
        stats = cgminer_command(ip, port, "devs", timeout)
        # Get pools for connection info
        pools = cgminer_command(ip, port, "pools", timeout)

        if "error" in summary:
            raise Exception(summary["error"])

        summary_data = summary.get("SUMMARY", [{}])[0]

        # Extract hashrate - Whatsminer reports in MH/s or GH/s
        mhs = _safe_num(summary_data.get('MHS av', 0)) or _safe_num(summary_data.get('MHS 5s', 0))
        ghs = _safe_num(summary_data.get('GHS av', 0)) or _safe_num(summary_data.get('GHS 5s', 0))

        if ghs and ghs > 0:
            hashrate_ghs = ghs
        else:
            hashrate_ghs = mhs / 1000 if mhs else 0

        hashrate_ths = hashrate_ghs / 1000

        # Extract device stats
        chip_temps = []
        board_temps = []
        fan_speeds = []
        power_watts = 0
        voltage = 0
        model = "Whatsminer"
        firmware = ""

        # Parse DEVS response for per-device stats
        if "DEVS" in stats:
            for dev in stats["DEVS"]:
                temp = _safe_num(dev.get("Temperature", 0))
                if temp and temp > 0:
                    chip_temps.append(temp)

        # Also check STATS if available
        stats_data = cgminer_command(ip, port, "stats", timeout)
        if "STATS" in stats_data:
            for stat in stats_data["STATS"]:
                # Model detection
                if "Type" in stat:
                    model = stat.get("Type", "Whatsminer")

                # Temperature sensors
                for i in range(1, 4):
                    temp = _safe_num(stat.get(f"temp{i}", 0))
                    if temp and temp > 0 and temp not in chip_temps:
                        chip_temps.append(temp)

                # Fan speeds
                for i in range(1, 5):
                    fan = _safe_num(stat.get(f"fan{i}", stat.get(f"fan_speed_in{i}", 0)))
                    if fan and fan > 0:
                        fan_speeds.append(fan)

                # Power
                power_watts = _safe_num(stat.get("Power", stat.get("power", 0)))
                if not power_watts:
                    # Some models report power differently
                    power_watts = _safe_num(stat.get("Power_RT", 0))

                # Voltage
                if voltage == 0:
                    v = _safe_num(stat.get("voltage", stat.get("Voltage", stat.get("psu_voltage", 0))))
                    if v and v > 0:
                        voltage = v

        # Get pool info
        pool_url = ""
        if "POOLS" in pools:
            for pool in pools["POOLS"]:
                if pool.get("Stratum Active"):
                    pool_url = pool.get("URL", "")
                    break

        max_chip_temp = max(chip_temps) if chip_temps else 0
        avg_fan_speed = sum(fan_speeds) // len(fan_speeds) if fan_speeds else 0

        return {
            "online": True,
            "hashrate_ghs": hashrate_ghs,
            "hashrate_ths": hashrate_ths,
            "power_watts": power_watts,
            "temps": {
                "chip": max_chip_temp,
                "chip_temps": chip_temps
            },
            "fans": {
                "speeds": fan_speeds,
                "avg_rpm": avg_fan_speed
            },
            "uptime": _safe_num(summary_data.get('Elapsed', 0)),
            "accepted": _safe_num(summary_data.get('Accepted', 0)),
            "rejected": _safe_num(summary_data.get('Rejected', 0)),
            "stale": _safe_num(summary_data.get('Stale', 0)),
            "best_diff": str(summary_data.get('Best Share', 0)),
            "difficulty": _safe_num(summary_data.get('Difficulty Accepted', 0)),
            "hardware_errors": _safe_num(summary_data.get('Hardware Errors', 0)),
            "pool_url": pool_url,
            "model": model,
            "firmware": firmware,
            "voltage": voltage
        }
    except Exception as e:
        return {
            "online": False,
            "error": str(e),
            "hashrate_ghs": 0,
            "hashrate_ths": 0,
            "power_watts": 0,
            "voltage": 0,
            "temps": {"chip": 0},
            "fans": {"speeds": [], "avg_rpm": 0},
            "uptime": 0,
            "accepted": 0,
            "rejected": 0,
            "stale": 0,
            "best_diff": "0",
            "hardware_errors": 0,
            "model": "Whatsminer"
        }


def fetch_innosilicon(ip, port=4028, timeout=5):
    """
    Fetch data from Innosilicon device via CGMiner API.

    Supported models: A10, A10 Pro, A11, T2T, T3, etc.

    Innosilicon miners use a standard CGMiner API on port 4028.
    Reference: CGMiner API documentation
    """
    # SECURITY: IP validation is handled in cgminer_command
    try:
        # Get summary for hashrate and share counts
        summary = cgminer_command(ip, port, "summary", timeout)
        # Get stats for hardware details
        stats = cgminer_command(ip, port, "stats", timeout)
        # Get pools for connection info
        pools = cgminer_command(ip, port, "pools", timeout)

        if "error" in summary:
            raise Exception(summary["error"])

        summary_data = summary.get("SUMMARY", [{}])[0]

        # Extract hashrate — Innosilicon firmware confirmed to return string-encoded numbers
        mhs = _safe_num(summary_data.get('MHS av', 0)) or _safe_num(summary_data.get('MHS 5s', 0))
        ghs = _safe_num(summary_data.get('GHS av', 0)) or _safe_num(summary_data.get('GHS 5s', 0))

        if ghs and ghs > 0:
            hashrate_ghs = ghs
        else:
            hashrate_ghs = mhs / 1000 if mhs else 0

        hashrate_ths = hashrate_ghs / 1000

        # Extract stats
        chip_temps = []
        board_temps = []
        fan_speeds = []
        power_watts = 0
        model = "Innosilicon"
        firmware = ""

        if "STATS" in stats:
            for stat in stats["STATS"]:
                # Model info
                if "Type" in stat:
                    model = stat.get("Type", "Innosilicon")
                if "miner_version" in stat:
                    firmware = stat.get("miner_version", "")

                # Temperatures — Innosilicon returns strings for these
                for i in range(1, 10):
                    temp = _safe_num(stat.get(f"temp{i}", 0))
                    if temp and temp > 0:
                        chip_temps.append(temp)

                # Also check for board temps
                for i in range(1, 10):
                    temp = _safe_num(stat.get(f"temp2_{i}", stat.get(f"temp_pcb{i}", 0)))
                    if temp and temp > 0:
                        board_temps.append(temp)

                # Fan speeds — Innosilicon returns strings for these
                for i in range(1, 5):
                    fan = _safe_num(stat.get(f"fan{i}", 0))
                    if fan and fan > 0:
                        fan_speeds.append(fan)

                # Power consumption — Innosilicon returns strings for this
                power_watts = _safe_num(stat.get("Power", stat.get("power", 0)))

        # Get pool info
        pool_url = ""
        if "POOLS" in pools:
            for pool in pools["POOLS"]:
                if pool.get("Stratum Active"):
                    pool_url = pool.get("URL", "")
                    break

        max_chip_temp = max(chip_temps) if chip_temps else 0
        max_board_temp = max(board_temps) if board_temps else 0
        avg_fan_speed = sum(fan_speeds) // len(fan_speeds) if fan_speeds else 0

        return {
            "online": True,
            "hashrate_ghs": hashrate_ghs,
            "hashrate_ths": hashrate_ths,
            "power_watts": power_watts,
            "temps": {
                "chip": max_chip_temp,
                "board": max_board_temp,
                "chip_temps": chip_temps,
                "board_temps": board_temps
            },
            "fans": {
                "speeds": fan_speeds,
                "avg_rpm": avg_fan_speed
            },
            "uptime": _safe_num(summary_data.get('Elapsed', 0)),
            "accepted": _safe_num(summary_data.get('Accepted', 0)),
            "rejected": _safe_num(summary_data.get('Rejected', 0)),
            "stale": _safe_num(summary_data.get('Stale', 0)),
            "best_diff": str(summary_data.get('Best Share', 0)),
            "difficulty": _safe_num(summary_data.get('Difficulty Accepted', 0)),
            "hardware_errors": _safe_num(summary_data.get('Hardware Errors', 0)),
            "pool_url": pool_url,
            "model": model,
            "firmware": firmware
        }
    except Exception as e:
        return {
            "online": False,
            "error": str(e),
            "hashrate_ghs": 0,
            "hashrate_ths": 0,
            "power_watts": 0,
            "temps": {"chip": 0, "board": 0},
            "fans": {"speeds": [], "avg_rpm": 0},
            "uptime": 0,
            "accepted": 0,
            "rejected": 0,
            "stale": 0,
            "best_diff": "0",
            "hardware_errors": 0,
            "model": "Innosilicon"
        }


def fetch_goldshell(ip, timeout=5):
    """
    Fetch data from Goldshell device via HTTP API.

    Supported models: KD6, LT5, Mini-DOGE, HS5, etc.

    Goldshell miners use HTTP API on port 80 with two endpoints:
      - /mcb/cgminer?cgminercmd=devs - Device stats (hashrate, temp, fanspeed, shares)
      - /mcb/status - Model/hardware info

    Reference: https://github.com/jorgedlcruz/goldshell-miner-grafana
    """
    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return {
            "online": False,
            "error": "Invalid or blocked IP address (SSRF protection)"
        }
    try:
        # Initialize result with defaults
        result = {
            "online": True,
            "hashrate_ghs": 0,
            "hashrate_ths": 0,
            "power_watts": 0,
            "temps": {"chip": 0, "board": 0, "all_temps": []},
            "fans": {"speeds": [], "avg_rpm": 0},
            "uptime": 0,
            "accepted": 0,
            "rejected": 0,
            "best_diff": "0",
            "pool_url": "",
            "model": "Goldshell",
            "voltage": 0
        }

        # Step 1: Get device stats from CGMiner-style API
        # This endpoint returns: hashrate, av_hashrate, temp, fanspeed, accepted, rejected
        devs_response = requests.get(
            f"http://{ip}/mcb/cgminer?cgminercmd=devs",
            timeout=timeout,
            headers={"User-Agent": "SpiralPool-Dashboard/1.0"}
        )

        if devs_response.status_code == 200:
            devs_data = devs_response.json()
            # Goldshell wraps response in 'data' key
            if "data" in devs_data:
                d = devs_data["data"]
                # Handle list response (multiple devices) or single dict
                if isinstance(d, list) and len(d) > 0:
                    d = d[0]  # Use first device
                if isinstance(d, dict):
                    # Hashrate - Goldshell reports in MH/s for Scrypt miners
                    hashrate = _safe_num(d.get("hashrate", d.get("av_hashrate", 0)))
                    if hashrate:
                        # Convert MH/s to GH/s
                        result["hashrate_ghs"] = hashrate / 1000
                        result["hashrate_ths"] = hashrate / 1000000

                    # Temperature - comes as string like "77.3 °C"
                    temp = d.get("temp", "")
                    if temp:
                        if isinstance(temp, str):
                            # Parse "77.3 °C" format
                            temp_val = temp.replace("°C", "").replace("°", "").strip()
                            try:
                                result["temps"]["chip"] = float(temp_val)
                                result["temps"]["all_temps"] = [float(temp_val)]
                            except ValueError:
                                pass
                        elif isinstance(temp, (int, float)):
                            result["temps"]["chip"] = float(temp)
                            result["temps"]["all_temps"] = [float(temp)]

                    # Fan speed - comes as string like "1560 rpm / 1500 rpm"
                    fanspeed = d.get("fanspeed", "")
                    if fanspeed:
                        if isinstance(fanspeed, str):
                            # Parse "1560 rpm / 1500 rpm" format
                            import re
                            fan_speeds = re.findall(r"(\d+)\s*rpm", fanspeed.lower())
                            if fan_speeds:
                                speeds = [int(s) for s in fan_speeds]
                                result["fans"]["speeds"] = speeds
                                result["fans"]["avg_rpm"] = sum(speeds) // len(speeds)
                        elif isinstance(fanspeed, (int, float)):
                            result["fans"]["speeds"] = [int(fanspeed)]
                            result["fans"]["avg_rpm"] = int(fanspeed)

                    # Share counts
                    result["accepted"] = _safe_num(d.get("accepted", 0))
                    result["rejected"] = _safe_num(d.get("rejected", 0))

                    # Uptime
                    result["uptime"] = _safe_num(d.get("time", d.get("uptime", 0)))

        # Step 2: Get status info (model, hardware)
        try:
            status_response = requests.get(
                f"http://{ip}/mcb/status",
                timeout=timeout,
                headers={"User-Agent": "SpiralPool-Dashboard/1.0"}
            )
            if status_response.status_code == 200:
                status_data = status_response.json()
                model = status_data.get("model", "Goldshell")
                result["model"] = f"Goldshell {model}" if not model.startswith("Goldshell") else model

                # Some models return temperature in status endpoint
                if "temperature" in status_data and result["temps"]["chip"] == 0:
                    result["temps"]["chip"] = _safe_num(status_data["temperature"])
                if "env_temp" in status_data:
                    result["temps"]["board"] = _safe_num(status_data["env_temp"])
        except Exception:
            pass  # Status endpoint is optional

        return result

    except Exception as e:
        return {
            "online": False,
            "error": str(e),
            "hashrate_ghs": 0,
            "hashrate_ths": 0,
            "power_watts": 0,
            "temps": {"chip": 0, "board": 0},
            "fans": {"speeds": [], "avg_rpm": 0},
            "uptime": 0,
            "accepted": 0,
            "rejected": 0,
            "best_diff": "0",
            "model": "Goldshell"
        }


def fetch_all_miners():
    """Fetch data from all configured miners.

    IMPORTANT: Only counts shares/stats from miners connected to the LOCAL Spiral Stratum pool.
    Miners connected to external pools are still shown but their shares
    don't count toward pool totals. This prevents mixing stats from multiple pools.
    """
    global miner_cache, lifetime_stats

    config = load_config()
    devices = config.get("devices", {})

    all_miners = {}
    totals = {
        "hashrate_ths": 0,
        "power_watts": 0,
        "accepted_shares": 0,
        "rejected_shares": 0,
        "blocks_found": 0,
        "online_count": 0,
        "total_count": 0,
        "local_pool_count": 0,  # Miners connected to local pool
        "external_pool_count": 0  # Miners connected to other pools
    }

    best_diff = "0"

    def process_miner(data, device, default_watts, hashrate_key="hashrate_ghs", ths_divisor=1000):
        """Helper to process miner data and update totals.

        Only counts shares from miners connected to the local Spiral Stratum pool.
        """
        nonlocal best_diff

        # Check if miner is connected to local pool
        pool_url = data.get("pool_url", "")
        is_local = is_miner_connected_to_local_pool(pool_url)
        data["is_local_pool"] = is_local
        data["pool_connection_status"] = "local" if is_local else ("external" if is_local is False else "unknown")

        totals["total_count"] += 1
        if data["online"]:
            totals["online_count"] += 1

            # Always count hashrate and power (these are about the hardware)
            if hashrate_key == "hashrate_ths":
                hr_ths = data.get(hashrate_key, 0)
                totals["hashrate_ths"] += hr_ths
            else:
                hr_ths = data.get(hashrate_key, 0) / ths_divisor
                totals["hashrate_ths"] += hr_ths

            # Record hashrate sample for degradation tracking
            # Skip ESP32 lottery miners — pool-reported kH/s hashrate fluctuates too wildly
            try:
                miner_ip = data.get("ip", "")
                if miner_ip and hr_ths > 0 and not data.get("no_http_api", False):
                    record_hashrate_sample(miner_ip, hr_ths, miner_name=data.get("name"))
            except Exception:
                pass
            # Use reported power if available and non-zero, otherwise fall back to configured/default watts
            # A miner that is online with hashrate drawing 0W is "not reported", not "actually 0W"
            reported_power = data.get("power_watts")
            effective_power = reported_power if reported_power else device.get("watts", default_watts)
            data["power_watts"] = effective_power
            totals["power_watts"] += effective_power

            # Track pool connection counts
            if is_local:
                totals["local_pool_count"] += 1
            elif is_local is False:
                totals["external_pool_count"] += 1

            # Count shares/blocks from miners connected to LOCAL pool OR unknown pool
            # Only exclude miners DEFINITIVELY connected to an external pool
            # This ensures shares are counted when:
            # - COUNT_ALL_SHARES=true (single-pool setups)
            # - Miner is confirmed local (is_local=True)
            # - Miner pool URL is unknown/empty (is_local=None) - likely local
            # Shares are NOT counted when:
            # - Miner is confirmed external (is_local=False)
            if _COUNT_ALL_SHARES or is_local is not False:  # True or None (unknown)
                totals["accepted_shares"] += data.get("accepted", 0)
                totals["rejected_shares"] += data.get("rejected", 0)

                # Track best difficulty
                try:
                    diff = float(str(data.get("best_diff", "0")).replace(",", ""))
                    if diff > float(best_diff.replace(",", "")):
                        best_diff = data.get("best_diff", "0")
                except (ValueError, TypeError, AttributeError):
                    pass

    # Fetch AxeOS/NMAXE devices
    for device in devices.get("axeos", []):
        ip = device["ip"]
        name = device.get("name") or get_worker_name_for_ip(ip, ip)
        data = fetch_axeos(ip)
        data["type"] = "AxeOS"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 80)
        all_miners[name] = data
        process_miner(data, device, 80)

    # Fetch NerdQAxe++ devices (uses same AxeOS API)
    for device in devices.get("nerdqaxe", []):
        ip = device["ip"]
        name = device.get("name") or get_worker_name_for_ip(ip, ip)
        data = fetch_axeos(ip)
        data["type"] = "NerdQAxe++"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 80)
        all_miners[name] = data
        process_miner(data, device, 80)

    # Fetch NMaxe devices (uses same AxeOS API)
    for device in devices.get("nmaxe", []):
        ip = device["ip"]
        name = device.get("name") or get_worker_name_for_ip(ip, ip)
        data = fetch_axeos(ip)
        data["type"] = "NMaxe"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 20)
        all_miners[name] = data
        process_miner(data, device, 20)

    # Fetch ESP32 Miner devices (ESP32-based, very low hashrate ~50-78 kH/s)
    # ESP32 Miner has NO HTTP API - we MUST poll the pool for stats.
    # IP-based detection is unreliable (pool may see all LAN miners from same
    # gateway IP), so we detect ESP32 miners by userAgent in the connections API.
    # Get cached ESP32 connection data from fetch_worker_name_mapping()
    fetch_worker_name_mapping()  # Ensure cache is populated
    esp32_conns = _worker_name_cache.get("esp32_connections", [])

    # Build a lookup of ESP32 connections by workerName (case-insensitive)
    esp32_conn_by_worker = {}
    for conn in esp32_conns:
        wname = conn.get("workerName", "default")
        esp32_conn_by_worker.setdefault(wname.lower(), []).append(conn)

    # Track which connections get claimed by configured devices
    claimed_conn_indices = set()

    esp32_devices = devices.get("esp32miner", [])
    for device in esp32_devices:
        ip = device["ip"]
        device_name = device.get("name")
        name = device_name or ip

        # ESP32 Miner has NO HTTP API - skip HTTP fetch entirely
        data = {"online": False, "no_http_api": True}

        # Match by worker name: device config "name" should match pool workerName
        esp32_conn = None
        if device_name:
            candidates = esp32_conn_by_worker.get(device_name.lower(), [])
            for c in candidates:
                c_id = id(c)
                if c_id not in claimed_conn_indices:
                    esp32_conn = c
                    claimed_conn_indices.add(c_id)
                    break

        # Fallback: if only one ESP32 connection and one configured device, match them
        if esp32_conn is None and len(esp32_devices) == 1 and len(esp32_conns) == 1:
            c_id = id(esp32_conns[0])
            if c_id not in claimed_conn_indices:
                esp32_conn = esp32_conns[0]
                claimed_conn_indices.add(c_id)

        if esp32_conn:
            data["online"] = True
            data["stratum_connected"] = True
            data["current_difficulty"] = esp32_conn.get("difficulty", 0)
            data["accepted"] = esp32_conn.get("shareCount", 0)
            data["user_agent"] = esp32_conn.get("userAgent", "")
            data["connected_at"] = esp32_conn.get("connectedAt", "")
            data["worker_name"] = esp32_conn.get("workerName", "default")

            # Get stats from pool worker stats API
            actual_worker = esp32_conn.get("workerName", "default")
            miner_address = esp32_conn.get("minerAddress", "")
            pool_stats = fetch_pool_worker_stats(actual_worker, miner_address)
            if pool_stats:
                current_hr = pool_stats.get("currentHashrate", 0)
                avg_hr = pool_stats.get("averageHashrate", 0)
                data["hashrate_ghs"] = (current_hr or avg_hr) / 1e9
                # For named workers, use pool share counts
                # For "default" workers, share counts are unreliable (aggregated bucket)
                if actual_worker != "default":
                    data["accepted"] = pool_stats.get("sharesAccepted", 0)
                    data["rejected"] = pool_stats.get("sharesRejected", 0)
                    data["best_diff"] = str(pool_stats.get("bestDifficulty", 0))
        elif is_esp32_connected_via_stratum():
            data["online"] = True
            data["stratum_connected"] = True

        data["type"] = "ESP32 Miner"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 2)
        all_miners[name] = data
        process_miner(data, device, 2)

    # Auto-discover ESP32 connections not matched to any configured device
    for conn in esp32_conns:
        if id(conn) in claimed_conn_indices:
            continue
        worker_name = conn.get("workerName", "default")
        # Use worker name as display name, or "ESP32-<index>" for "default" workers
        if worker_name and worker_name != "default":
            auto_name = worker_name
        else:
            # Count how many unnamed ESP32s we've seen
            auto_idx = sum(1 for n in all_miners if n.startswith("ESP32-"))
            auto_name = f"ESP32-{auto_idx + 1}"

        data = {
            "online": True,
            "no_http_api": True,
            "stratum_connected": True,
            "current_difficulty": conn.get("difficulty", 0),
            "accepted": conn.get("shareCount", 0),
            "user_agent": conn.get("userAgent", ""),
            "connected_at": conn.get("connectedAt", ""),
            "worker_name": worker_name,
            "type": "ESP32 Miner",
            "name": auto_name,
            "ip": "auto-detected",
            "configured_watts": 2,
            "auto_discovered": True,
        }

        # Get stats from pool worker stats API
        actual_worker = conn.get("workerName", "default")
        miner_address = conn.get("minerAddress", "")
        pool_stats = fetch_pool_worker_stats(actual_worker, miner_address)
        if pool_stats:
            current_hr = pool_stats.get("currentHashrate", 0)
            avg_hr = pool_stats.get("averageHashrate", 0)
            data["hashrate_ghs"] = (current_hr or avg_hr) / 1e9
            if actual_worker != "default":
                data["accepted"] = pool_stats.get("sharesAccepted", 0)
                data["rejected"] = pool_stats.get("sharesRejected", 0)
                data["best_diff"] = str(pool_stats.get("bestDifficulty", 0))

        all_miners[auto_name] = data
        # Use a synthetic device dict for process_miner
        process_miner(data, {"ip": "auto-detected", "watts": 2}, 2)

    # Fetch QAxe devices (quad-ASIC, ~2 TH/s - uses AxeOS API)
    for device in devices.get("qaxe", []):
        ip = device["ip"]
        name = device.get("name") or get_worker_name_for_ip(ip, ip)
        data = fetch_axeos(ip)
        data["type"] = "QAxe"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 80)
        all_miners[name] = data
        process_miner(data, device, 80)

    # Fetch QAxe+ devices (enhanced cooling variant - uses AxeOS API)
    for device in devices.get("qaxeplus", []):
        ip = device["ip"]
        name = device.get("name") or get_worker_name_for_ip(ip, ip)
        data = fetch_axeos(ip)
        data["type"] = "QAxe+"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 100)
        all_miners[name] = data
        process_miner(data, device, 100)

    # Fetch Avalon devices
    for device in devices.get("avalon", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_avalon(ip, port)

        # Determine the display name with priority:
        # 1. Configured name in device settings
        # 2. Worker name extracted from pool_user (ADDRESS.workername format)
        # 3. Worker name from stratum connections mapping
        # 4. IP address as fallback
        name = device.get("name")
        if not name:
            # Try to extract worker name from CGMiner's pool user (e.g., "dgb1qxyz...Heat2Sats")
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            # Try stratum connections mapping
            name = get_worker_name_for_ip(ip, None)
        if not name:
            # Fallback to IP
            name = ip

        # Use model from API response if available, otherwise default to "Avalon"
        data["type"] = data.get("model", "Avalon")
        data["name"] = name
        data["ip"] = device["ip"]
        # Use configured watts, default 140W for Avalon Nano 3 (most common small Avalon)
        configured_watts = device.get("watts", 140)
        data["configured_watts"] = configured_watts
        # Add voltage if available from CGMiner stats
        if data.get("voltage"):
            data["voltage"] = data["voltage"]
        all_miners[name] = data
        process_miner(data, device, configured_watts, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Canaan AvalonMiner devices (A13, A14 series - CGMiner API)
    for device in devices.get("canaan", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_avalon(ip, port)  # Uses same CGMiner API as Avalon

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = data.get("model", "Canaan AvalonMiner")
        data["name"] = name
        data["ip"] = device["ip"]
        configured_watts = device.get("watts", 3000)
        data["configured_watts"] = configured_watts
        all_miners[name] = data
        process_miner(data, device, configured_watts, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Bitmain Antminer devices (S19, S21, T21, etc.)
    for device in devices.get("antminer", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_antminer(ip, port)

        # Determine name with same priority as Avalon
        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "Antminer"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 3250)
        all_miners[name] = data
        process_miner(data, device, 3250, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch MicroBT Whatsminer devices (M30, M50, M60, etc.)
    for device in devices.get("whatsminer", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_whatsminer(ip, port)

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "Whatsminer"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 3400)
        all_miners[name] = data
        process_miner(data, device, 3400, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Innosilicon devices (A10, A11, T2T, T3 — CGMiner on 4028 disabled by default; enable in web UI)
    for device in devices.get("innosilicon", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_innosilicon(ip, port)

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "Innosilicon"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 3500)
        all_miners[name] = data
        process_miner(data, device, 3500, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Goldshell devices (KD6, LT5, Mini-DOGE, HS5, etc.)
    for device in devices.get("goldshell", []):
        ip = device["ip"]
        data = fetch_goldshell(ip)

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "Goldshell"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 2300)
        all_miners[name] = data
        process_miner(data, device, 2300, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Hammer Miner devices (PlebSource Hammer - Scrypt miners, uses AxeOS API)
    for device in devices.get("hammer", []):
        ip = device["ip"]
        data = fetch_axeos(ip)

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", data.get("stratumUser", ""))
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "Hammer Miner"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 25)
        all_miners[name] = data
        process_miner(data, device, 25)

    # Fetch FutureBit Apollo devices (SHA-256d, uses BFGMiner/CGMiner-compatible API)
    # Note: Apollo uses BFGMiner internally which is CGMiner-compatible on port 4028
    # Also has GraphQL API on port 5000 for web UI, but we use mining API for compatibility
    # Reference: https://github.com/jstefanop/apolloapi-v2
    for device in devices.get("futurebit", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_avalon(ip, port)  # BFGMiner is CGMiner-compatible

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "FutureBit Apollo"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 200)
        all_miners[name] = data
        process_miner(data, device, 200, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Bitmain Antminer Scrypt devices (L7, L9 - uses same CGMiner API)
    for device in devices.get("antminer_scrypt", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_antminer(ip, port)

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "Antminer Scrypt"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 3250)
        all_miners[name] = data
        process_miner(data, device, 3250, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch BraiinsOS devices (S9, S17, S19, S21 with Braiins firmware)
    for device in devices.get("braiins", []):
        ip = device["ip"]
        username = device.get("username", "root")
        password = device.get("password", "")
        data = fetch_braiins(ip, username=username, password=password)

        name = device.get("name")
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        # Use model from API if available
        data["type"] = data.get("model", "BraiinsOS")
        data["name"] = name
        data["ip"] = device["ip"]
        # Default watts varies by model - S19 Pro ~3250W, S9 ~1350W
        data["configured_watts"] = device.get("watts", 3250)
        all_miners[name] = data
        process_miner(data, device, 3250, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Vnish firmware devices (Antminers with Vnish custom firmware)
    for device in devices.get("vnish", []):
        ip = device["ip"]
        password = device.get("password", "admin")
        data = fetch_vnish(ip, password=password)

        name = device.get("name")
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = data.get("model", "Vnish")
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 3250)
        all_miners[name] = data
        process_miner(data, device, 3250, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch LuxOS firmware devices (Antminers with LuxOS firmware)
    for device in devices.get("luxos", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_luxos(ip, port=port)

        name = device.get("name")
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "LuxOS"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 3250)
        all_miners[name] = data
        process_miner(data, device, 3250, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Lucky Miner devices (LV06/LV07/LV08 - uses AxeOS API)
    for device in devices.get("luckyminer", []):
        ip = device["ip"]
        data = fetch_axeos(ip)  # Uses same API as BitAxe

        name = device.get("name")
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        model = data.get("model", "Lucky Miner")
        if "lv08" in model.lower():
            data["type"] = "Lucky Miner LV08"
            default_watts = 200
        elif "lv07" in model.lower():
            data["type"] = "Lucky Miner LV07"
            default_watts = 50
        else:
            data["type"] = "Lucky Miner LV06"
            default_watts = 25
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", default_watts)
        all_miners[name] = data
        process_miner(data, device, default_watts, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Jingle Miner devices (BTC Solo Pro/Lite - uses AxeOS API)
    for device in devices.get("jingleminer", []):
        ip = device["ip"]
        data = fetch_axeos(ip)

        name = device.get("name")
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        model = data.get("model", "Jingle Miner")
        if "pro" in model.lower():
            data["type"] = "Jingle Miner Pro"
            default_watts = 200
        else:
            data["type"] = "Jingle Miner Lite"
            default_watts = 50
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", default_watts)
        all_miners[name] = data
        process_miner(data, device, default_watts, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Zyber devices (8G/8GP/8S - uses AxeOS API, TinyChipHub)
    for device in devices.get("zyber", []):
        ip = device["ip"]
        data = fetch_axeos(ip)

        name = device.get("name")
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        model = data.get("model", "Zyber")
        data["type"] = f"Zyber {model}" if "zyber" not in model.lower() else model
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 100)
        all_miners[name] = data
        process_miner(data, device, 100, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch GekkoScience devices (Compac F, NewPac, R606 - CGMiner API)
    for device in devices.get("gekkoscience", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = cgminer_command(ip, port, "summary")

        name = device.get("name")
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        # Parse CGMiner response
        if not data.get("error"):
            summary_data = data.get("SUMMARY", [{}])[0]
            ghs = summary_data.get('GHS av', summary_data.get('GHS 5s', 0))
            if not ghs:
                mhs = summary_data.get('MHS av', summary_data.get('MHS 5s', 0))
                ghs = float(mhs or 0) / 1000
            ghs = float(ghs or 0)
            data = {
                "online": True,
                "hashrate_ths": ghs / 1000,
                "hashrate_ghs": ghs,
                "power_watts": device.get("watts", 5),
                "temps": {"chip": 0, "board": 0},
                "uptime": summary_data.get("Elapsed", 0),
                "accepted": summary_data.get("Accepted", 0),
                "rejected": summary_data.get("Rejected", 0),
                "best_diff": str(summary_data.get("Best Share", 0))
            }
        else:
            data = {"online": False, "error": data.get("error"), "hashrate_ths": 0}

        data["type"] = "GekkoScience"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 5)
        all_miners[name] = data
        process_miner(data, device, 5, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch iPollo devices (V1, V1 Mini, G1 — CGMiner best-effort; uses LuCI web on port 80 primarily)
    for device in devices.get("ipollo", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_antminer(ip, port)  # Uses same CGMiner API

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "iPollo"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 2000)
        all_miners[name] = data
        process_miner(data, device, 2000, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Ebang/Ebit devices (E9/E10/E11/E12 - CGMiner API)
    for device in devices.get("ebang", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_antminer(ip, port)

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "Ebang/Ebit"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 2800)
        all_miners[name] = data
        process_miner(data, device, 2800, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch ePIC BlockMiner devices (HTTP REST API on port 4028 — NOT CGMiner TCP)
    for device in devices.get("epic", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_epic_http(ip, port, username=device.get("username", "root"), password=device.get("password", "letmein"))

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "ePIC BlockMiner"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 3000)
        all_miners[name] = data
        process_miner(data, device, 3000, hashrate_key="hashrate_ths", ths_divisor=1)

    # Fetch Elphapex devices (DG1, DG Home - Scrypt miners)
    # NOTE: Elphapex primarily uses LuCI web API on port 80. CGMiner on 4028 is best-effort.
    for device in devices.get("elphapex", []):
        ip = device["ip"]
        port = device.get("port", 4028)
        data = fetch_antminer(ip, port)

        name = device.get("name")
        if not name:
            pool_user = data.get("pool_user", "")
            name = extract_worker_from_pool_user(pool_user)
        if not name:
            name = get_worker_name_for_ip(ip, None)
        if not name:
            name = ip

        data["type"] = "Elphapex"
        data["name"] = name
        data["ip"] = device["ip"]
        data["configured_watts"] = device.get("watts", 3000)
        all_miners[name] = data
        process_miner(data, device, 3000, hashrate_key="hashrate_ths", ths_divisor=1)

    # Get pool's best share difficulty from Prometheus (authoritative source)
    # This is the actual best share submitted to the pool, not miner-reported
    prometheus_metrics = fetch_prometheus_metrics()
    pool_best_diff = prometheus_metrics.get("stratum_best_share_difficulty", 0)

    # Pool blocks: session count from Prometheus (resets on stratum restart)
    # Lifetime total is tracked separately in lifetime_stats (persistent file)
    pool_blocks = int(prometheus_metrics.get("stratum_blocks_found_total", 0))
    totals["blocks_found"] = pool_blocks

    # Use pool best diff if available, otherwise fall back to miner-reported
    # This ensures we show actual pool data, not inflated miner self-reported values
    if pool_best_diff and float(pool_best_diff) > 0:
        totals["best_diff"] = str(pool_best_diff)
    else:
        totals["best_diff"] = best_diff  # Fallback to miner-reported

    # Update lifetime stats atomically with lock
    # Use max of Prometheus (session), database (all blocks), and current lifetime
    # so lifetime total never goes backwards on stratum restart
    pool_accepted_total = int(prometheus_metrics.get("stratum_shares_accepted_total", 0))
    with _lifetime_stats_lock:
        if totals["accepted_shares"] > lifetime_stats.get("total_shares", 0):
            lifetime_stats["total_shares"] = totals["accepted_shares"]
        # Track pool-accepted shares separately (authoritative — only shares THIS pool accepted)
        if pool_accepted_total > lifetime_stats.get("total_pool_shares", 0):
            lifetime_stats["total_pool_shares"] = pool_accepted_total
        db_blocks = pool_stats_cache.get("blocks_found", 0)
        best_blocks = max(totals["blocks_found"], db_blocks, lifetime_stats.get("total_blocks", 0))
        if best_blocks > lifetime_stats.get("total_blocks", 0):
            lifetime_stats["total_blocks"] = best_blocks

        # Use pool's best share difficulty as authoritative source
        # Only update lifetime if pool reports a higher value than stored
        try:
            # Pool best diff is a float, convert to comparable format
            current_best = float(pool_best_diff) if pool_best_diff else 0
            lifetime_best = float(str(lifetime_stats.get("best_share_difficulty", "0")).replace(",", ""))
            if current_best > lifetime_best:
                # Store as formatted string for display consistency
                lifetime_stats["best_share_difficulty"] = str(current_best)
        except (ValueError, TypeError, AttributeError):
            pass

        save_stats()

    # Update miner cache atomically with lock
    with _miner_cache_lock:
        old_miners = miner_cache.get("miners", {})
        miner_cache["last_update"] = time.time()
        miner_cache["miners"] = all_miners.copy()
        miner_cache["totals"] = totals.copy()

    # Track miner online/offline transitions for activity feed (outside lock)
    # Skip on first poll (old_miners empty) to avoid flooding feed on startup
    if old_miners:
        for name, new_data in all_miners.items():
            old_data = old_miners.get(name)
            if old_data is None:
                continue  # Newly added miner — don't log as "came online"
            was_online = old_data.get("online", False)
            now_online = new_data.get("online", False)
            if was_online != now_online:
                ip = new_data.get("ip", "")
                record_miner_status(ip, now_online, miner_name=name)
        # Check miners that disappeared (removed from config or no longer responding)
        for name, old_data in old_miners.items():
            if name not in all_miners and old_data.get("online", False):
                ip = old_data.get("ip", "")
                record_miner_status(ip, False, miner_name=name)
        # Save if any transitions happened
        if any(
            old_miners.get(n, {}).get("online") != all_miners.get(n, {}).get("online")
            for n in set(list(all_miners.keys()) + list(old_miners.keys()))
            if n in old_miners
        ):
            save_activity_feed()

    return all_miners, totals


# ============================================
# ROUTES
# ============================================

# ─────────────────────────────────────────────
# AUTHENTICATION ROUTES
# ─────────────────────────────────────────────

@app.route('/login', methods=['GET', 'POST'])
def login():
    """Login page and handler."""
    # If already logged in, redirect to dashboard
    if current_user.is_authenticated:
        return redirect(url_for('index'))

    error = None
    if request.method == 'POST':
        client_ip = request.remote_addr or 'unknown'

        # Check rate limit
        allowed, remaining_seconds = check_login_rate_limit(client_ip)
        if not allowed:
            app.logger.warning(f"SECURITY: Rate limited login attempt from {client_ip}")
            # Display actual remaining lockout time
            if remaining_seconds >= 60:
                minutes = remaining_seconds // 60
                seconds = remaining_seconds % 60
                if seconds > 0:
                    time_msg = f"{minutes} minute{'s' if minutes != 1 else ''} and {seconds} second{'s' if seconds != 1 else ''}"
                else:
                    time_msg = f"{minutes} minute{'s' if minutes != 1 else ''}"
            else:
                time_msg = f"{remaining_seconds} second{'s' if remaining_seconds != 1 else ''}"
            error = f"Too many login attempts. Please try again in {time_msg}."
            return render_template('login.html', error=error, lockout_remaining=remaining_seconds), 429

        password = request.form.get('password', '')

        # For first-time setup, create password
        if is_first_time_setup():
            confirm_password = request.form.get('confirm_password', '')
            if not password:
                error = "Password is required"
            elif len(password) < 8:
                error = "Password must be at least 8 characters"
            elif password != confirm_password:
                error = "Passwords do not match"
            else:
                # Save new password
                password_hash = hash_password(password)
                save_auth_config({"password_hash": password_hash, "created_at": time.time()})
                app.logger.info(f"SECURITY: Admin password configured from {client_ip}")

                # Ensure first_run is true so miner setup wizard shows after first login
                config = load_config()
                config["first_run"] = True
                save_config(config)

                # Log in the user
                user = AdminUser("admin")
                # SECURITY: Regenerate session after login to prevent session fixation
                session.clear()
                login_user(user, remember=True)
                session.permanent = True
                record_login_attempt(client_ip, True)

                # Redirect to setup wizard directly after first-time password creation
                return redirect(url_for('setup'))
        else:
            # Normal login
            auth_config = load_auth_config()
            stored_hash = auth_config.get("password_hash", "")

            # If using env var password, verify against it using constant-time comparison
            # SECURITY: Use hmac.compare_digest to prevent timing attacks
            if ADMIN_PASSWORD:
                if hmac.compare_digest(password.encode('utf-8'), ADMIN_PASSWORD.encode('utf-8')):
                    user = AdminUser("admin")
                    # SECURITY: Regenerate session after login to prevent session fixation
                    session.clear()
                    login_user(user, remember=True)
                    session.permanent = True
                    record_login_attempt(client_ip, True)
                    app.logger.info(f"SECURITY: Successful login from {client_ip}")

                    return redirect(get_safe_redirect_url())
                else:
                    record_login_attempt(client_ip, False)
                    app.logger.warning(f"SECURITY: Failed login attempt from {client_ip}")
                    error = "Invalid password"
            elif stored_hash and verify_password(password, stored_hash):
                user = AdminUser("admin")
                # SECURITY: Regenerate session after login to prevent session fixation
                session.clear()
                login_user(user, remember=True)
                session.permanent = True
                record_login_attempt(client_ip, True)
                app.logger.info(f"SECURITY: Successful login from {client_ip}")

                return redirect(get_safe_redirect_url())
            else:
                record_login_attempt(client_ip, False)
                app.logger.warning(f"SECURITY: Failed login attempt from {client_ip}")
                error = "Invalid password"

    return render_template('login.html',
                           error=error,
                           first_time_setup=is_first_time_setup(),
                           auth_enabled=AUTH_ENABLED)


@app.route('/logout')
def logout():
    """Logout and redirect to login page."""
    logout_user()
    session.clear()
    return redirect(url_for('login'))


@app.route('/api/auth/status', methods=['GET'])
@api_key_or_login_required
def auth_status():
    """Check authentication status."""
    return jsonify({
        "authenticated": current_user.is_authenticated,
        "auth_enabled": AUTH_ENABLED,
        "user": current_user.username if current_user.is_authenticated else None
    })


@app.route('/api/auth/change-password', methods=['POST'])
@admin_required
def change_password():
    """Change admin password."""
    data = request.get_json()
    if not data:
        return jsonify({"error": "No data provided"}), 400

    current_password = data.get('current_password', '')
    new_password = data.get('new_password', '')
    confirm_password = data.get('confirm_password', '')

    # Validate inputs
    if not new_password:
        return jsonify({"error": "New password is required"}), 400
    if len(new_password) < 8:
        return jsonify({"error": "Password must be at least 8 characters"}), 400
    if new_password != confirm_password:
        return jsonify({"error": "Passwords do not match"}), 400

    # Verify current password
    if ADMIN_PASSWORD:
        if not hmac.compare_digest(current_password, ADMIN_PASSWORD):
            return jsonify({"error": "Current password is incorrect"}), 401
    else:
        auth_config = load_auth_config()
        stored_hash = auth_config.get("password_hash", "")
        if stored_hash and not verify_password(current_password, stored_hash):
            return jsonify({"error": "Current password is incorrect"}), 401

    # Save new password
    new_hash = hash_password(new_password)
    auth_config = load_auth_config()
    auth_config["password_hash"] = new_hash
    auth_config["updated_at"] = time.time()
    save_auth_config(auth_config)

    app.logger.info(f"SECURITY: Admin password changed by {request.remote_addr}")
    return jsonify({"success": True, "message": "Password changed successfully"})


# ─────────────────────────────────────────────
# MAIN ROUTES
# ─────────────────────────────────────────────

@app.route('/service-worker.js')
def service_worker():
    """Serve service worker from root scope so it controls all pages."""
    return send_from_directory(app.static_folder, 'service-worker.js',
                               mimetype='application/javascript')


@app.route('/')
@api_key_or_login_required
def index():
    """Main dashboard or setup wizard"""
    config = load_config()

    if config.get("first_run", True):
        return redirect(url_for('setup'))

    return render_template('dashboard.html', config=config)


@app.route('/setup')
@api_key_or_login_required
def setup():
    """First-time setup wizard"""
    config = load_config()
    return render_template('setup.html', config=config)


@app.route('/api/config', methods=['GET'])
@api_key_or_login_required
def get_config():
    """Get current configuration"""
    config = load_config()
    # SECURITY: Strip secrets before returning to client
    for secret_key in ("pool_admin_api_key", "metrics_auth_token"):
        if secret_key in config:
            config[secret_key] = "***REDACTED***" if config[secret_key] else ""
    # Strip device passwords
    for dtype, devices in config.get("devices", {}).items():
        if isinstance(devices, list):
            for device in devices:
                if isinstance(device, dict) and "password" in device:
                    device["password"] = "***REDACTED***"
    return jsonify(config)


@app.route('/api/config/server-mode', methods=['GET'])
@api_key_or_login_required
def get_server_mode():
    """Detect the current pool mode from the running stratum server.

    Queries the stratum server's /api/pools endpoint to determine:
    - How many pools are running (1 = solo mode, 2+ = multi-coin mode)
    - Which coin(s) are enabled
    - Returns coin configuration including wallet addresses

    Multi-coin mode support:
    - Returns detected_coins array with enabled coin symbols
    - Returns coins_config array with wallet addresses for settings page

    Returns:
        JSON with detected mode, enabled coins, and wallet config
    """
    # SECURITY: Whitelist of valid coin symbols (SHA-256d + Scrypt) and their variants
    VALID_COINS = {
        # Standard symbols
        "DGB", "BTC", "BCH", "BC2", "LTC", "DOGE", "DGB-SCRYPT",
        "PEP", "CAT", "NMC", "SYS", "XMY", "FBTC", "QBX",
        # Full names
        "DIGIBYTE", "BITCOIN", "BITCOINCASH", "BITCOIN-CASH",
        "BITCOINII", "BITCOIN-II", "BITCOIN2",
        "LITECOIN", "DOGECOIN", "DIGIBYTE-SCRYPT",
        "PEPECOIN", "CATCOIN",
        "NAMECOIN", "SYSCOIN", "MYRIADCOIN", "MYRIAD",
        "FRACTALBITCOIN", "FRACTAL",
        "QBITX", "Q-BITX"
    }

    def normalize_coin(coin_type):
        """Normalize coin names to standard symbols."""
        coin_type = coin_type.upper()
        # Use dict for exact key matching (order doesn't matter)
        coin_map = {
            "DIGIBYTE": "DGB", "DGB": "DGB",
            "BITCOINCASH": "BCH", "BITCOIN-CASH": "BCH", "BCH": "BCH",
            "BITCOINII": "BC2", "BITCOIN-II": "BC2", "BITCOIN2": "BC2", "BC2": "BC2", "BCII": "BC2",
            "BITCOIN": "BTC", "BTC": "BTC",
            "LITECOIN": "LTC", "LTC": "LTC",
            "DOGECOIN": "DOGE", "DOGE": "DOGE",
            "DIGIBYTE-SCRYPT": "DGB-SCRYPT", "DGB-SCRYPT": "DGB-SCRYPT",
            "PEPECOIN": "PEP", "PEP": "PEP", "MEME": "PEP",
            "CATCOIN": "CAT", "CAT": "CAT",
            "NAMECOIN": "NMC", "NMC": "NMC",
            "SYSCOIN": "SYS", "SYS": "SYS",
            "MYRIADCOIN": "XMY", "MYRIAD": "XMY", "XMY": "XMY",
            "FRACTALBITCOIN": "FBTC", "FRACTAL": "FBTC", "FBTC": "FBTC",
            "QBITX": "QBX", "Q-BITX": "QBX", "QBX": "QBX",
        }
        return coin_map.get(coin_type, coin_type)

    try:
        # Query the stratum server
        response = requests.get(f"{POOL_API_URL}/api/pools", timeout=5)
        if response.status_code != 200:
            return jsonify({
                "success": False,
                "error": "Could not reach stratum server",
                "detected_mode": None,
                "detected_coins": [],
                "coins_config": [],
                "merge_mining": None
            })

        data = response.json()
        pools = data.get("pools", [])

        # SECURITY: Validate pools is a list
        if not isinstance(pools, list):
            return jsonify({
                "success": False,
                "error": "Invalid response from stratum server",
                "detected_mode": None,
                "detected_coins": [],
                "coins_config": [],
                "merge_mining": None
            })

        if not pools:
            return jsonify({
                "success": False,
                "error": "No pools configured on server",
                "detected_mode": None,
                "detected_coins": [],
                "coins_config": [],
                "merge_mining": None
            })

        # Extract coins from pools with validation
        detected_coins = []
        coins_config = []
        pool_addresses = {}
        merge_mining_info = None
        COIN_WHITELIST = {"DGB", "BTC", "BCH", "BC2", "LTC", "DOGE", "DGB-SCRYPT", "PEP", "CAT", "NMC", "SYS", "XMY", "FBTC", "QBX"}

        for pool in pools:
            if not isinstance(pool, dict):
                continue
            coin_info = pool.get("coin", {})
            if not isinstance(coin_info, dict):
                continue
            coin_type = coin_info.get("type", "")
            if not isinstance(coin_type, str):
                continue
            coin_type = normalize_coin(coin_type)

            # SECURITY: Only accept whitelisted coin types (SHA256d + Scrypt)
            if coin_type in COIN_WHITELIST and coin_type not in detected_coins:
                detected_coins.append(coin_type)

                # Extract wallet address from pool config
                wallet_address = pool.get("address", "")
                pool_addresses[coin_type] = wallet_address

                coins_config.append({
                    "symbol": coin_type,
                    "wallet_address": wallet_address,
                    "pool_id": pool.get("id", ""),
                    "stratum_port": pool.get("ports", {}).get("default", {}).get("port", 3333)
                })

            # Extract merge mining info if present
            mm = pool.get("mergeMining")
            if isinstance(mm, dict) and mm.get("enabled") is True:
                aux_chains_raw = mm.get("auxChains", [])
                if isinstance(aux_chains_raw, list):
                    # SECURITY: Validate aux chain symbols through whitelist
                    # auxChains is [{symbol, address}, ...] from stratum API
                    valid_aux = []
                    for aux in aux_chains_raw:
                        if isinstance(aux, dict):
                            sym = normalize_coin(aux.get("symbol", ""))
                            addr = aux.get("address", "")
                        elif isinstance(aux, str):
                            # Backwards compat with older stratum binaries
                            sym = normalize_coin(aux)
                            addr = ""
                        else:
                            continue
                        if sym not in COIN_WHITELIST:
                            continue
                        valid_aux.append(sym)
                        # Add aux chain wallet to coins_config so frontend can pre-populate
                        if sym not in detected_coins:
                            detected_coins.append(sym)
                        wallet_address = str(addr).strip() if addr else ""
                        pool_addresses[sym] = wallet_address
                        coins_config.append({
                            "symbol": sym,
                            "wallet_address": wallet_address,
                            "pool_id": "",
                            "stratum_port": 0
                        })
                    if valid_aux:
                        merge_mining_info = {
                            "enabled": True,
                            "parent_coin": coin_type,
                            "aux_chains": valid_aux
                        }

        # Determine mode: merge > multi > solo
        if merge_mining_info:
            detected_mode = "merge"
        elif len(detected_coins) > 1:
            detected_mode = "multi"
        else:
            detected_mode = "solo"

        return jsonify({
            "success": True,
            "detected_mode": detected_mode,
            "detected_coins": detected_coins,
            "coins_config": coins_config,
            "merge_mining": merge_mining_info,
            "pool_count": len(pools),
            "primary_coin": sorted(detected_coins)[0] if detected_coins else None
        })

    except requests.exceptions.Timeout:
        return jsonify({
            "success": False,
            "error": "Stratum server timeout",
            "detected_mode": None,
            "detected_coins": [],
            "coins_config": [],
            "merge_mining": None
        })
    except Exception as e:
        return jsonify({
            "success": False,
            "error": str(e),
            "detected_mode": None,
            "detected_coins": [],
            "coins_config": [],
            "merge_mining": None
        })


@app.route('/api/config', methods=['POST'])
@admin_required
def update_config():
    """Update configuration with security validation"""
    # Rate limiting check
    client_ip = request.remote_addr or "unknown"
    if not check_rate_limit(client_ip, "config"):
        return jsonify({"success": False, "error": "Rate limit exceeded. Please wait before trying again."}), 429

    # Validate JSON payload
    new_config = request.json
    if not new_config or not isinstance(new_config, dict):
        return jsonify({"success": False, "error": "Invalid configuration data"}), 400

    # Validate pool_mode if provided
    pool_mode = new_config.get("pool_mode")
    if pool_mode and pool_mode not in ("solo", "multi"):
        return jsonify({"success": False, "error": "Invalid pool mode. Must be 'solo' or 'multi'."}), 400

    # Validate coins if provided
    coins = new_config.get("coins", [])
    if coins:
        if not isinstance(coins, list) or len(coins) > 3:
            return jsonify({"success": False, "error": "Invalid coins configuration"}), 400

        # Validate each coin
        for coin_config in coins:
            if not isinstance(coin_config, dict):
                return jsonify({"success": False, "error": "Invalid coin configuration format"}), 400

            symbol = coin_config.get("symbol", "").upper()
            if not validate_coin_symbol(symbol):
                return jsonify({"success": False, "error": f"Invalid coin symbol: {symbol}"}), 400

            # Validate wallet address format
            wallet_address = coin_config.get("wallet_address", "")
            if wallet_address:
                if not validate_wallet_address(symbol, wallet_address):
                    return jsonify({
                        "success": False,
                        "error": f"Invalid {symbol} wallet address format"
                    }), 400
                # Sanitize the address
                coin_config["wallet_address"] = sanitize_string(wallet_address, 128)

            # Normalize symbol to uppercase
            coin_config["symbol"] = symbol

        # Validate coin count for mode
        if pool_mode == "solo" and len(coins) != 1:
            return jsonify({"success": False, "error": "Solo mode requires exactly one coin"}), 400
        if pool_mode == "multi" and len(coins) < 2:
            return jsonify({"success": False, "error": "Multi-coin mode requires at least 2 coins"}), 400

    # Sanitize dashboard title if provided
    if "dashboard_title" in new_config:
        new_config["dashboard_title"] = sanitize_string(new_config["dashboard_title"], 64)

    config = load_config()

    # Track if pool mode changed (requires restart)
    old_pool_mode = config.get("pool_mode", "solo")
    old_coins = [c.get("symbol") for c in config.get("coins", [])]
    new_pool_mode = new_config.get("pool_mode", old_pool_mode)
    new_coins = [c.get("symbol") for c in new_config.get("coins", [])]

    # First-run completion: services need restart to pick up sentinel config,
    # expected hashrate, and any settings configured during initial setup
    is_first_run_completion = config.get("first_run", True) and new_config.get("devices")

    requires_restart = (
        old_pool_mode != new_pool_mode or
        set(old_coins) != set(new_coins) or
        is_first_run_completion
    )

    # Merge new config (only allowed keys)
    allowed_keys = {"dashboard_title", "pool_mode", "multi_coin_enabled", "coins", "devices",
                    "refresh_interval", "theme", "power_cost", "first_run", "expected_fleet_ths"}
    for key, value in new_config.items():
        if key in allowed_keys:
            config[key] = value

    # Validate refresh_interval range (5-300 seconds)
    if "refresh_interval" in config:
        try:
            ri = int(config["refresh_interval"])
            config["refresh_interval"] = max(5, min(ri, 300))
        except (ValueError, TypeError):
            config["refresh_interval"] = 30

    # Save expected_fleet_ths to sentinel config if provided
    if "expected_fleet_ths" in new_config:
        try:
            save_sentinel_expected_hashrate(new_config.get("expected_fleet_ths"))
        except Exception as e:
            app.logger.error(f"Failed to save expected hashrate to sentinel config: {str(e)}")

    # Mark first run complete if devices are configured
    if config.get("devices"):
        has_devices = any(
            len(config["devices"].get(dtype, [])) > 0
            for dtype in ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "avalon", "antminer", "antminer_scrypt", "whatsminer", "innosilicon", "goldshell", "hammer", "futurebit", "braiins", "vnish", "luxos", "luckyminer", "jingleminer", "zyber", "gekkoscience", "ipollo", "ebang", "epic", "elphapex", "canaan"]
        )
        if has_devices:
            config["first_run"] = False

    # Save pool config to the main pool config file if coins changed
    if new_config.get("coins"):
        try:
            save_pool_coin_config(new_config.get("coins", []), new_config.get("multi_coin_enabled", False))
        except Exception as e:
            # Log error but return generic message
            app.logger.error(f"Config save error: {str(e)}")
            return jsonify({"success": False, "error": "Failed to save pool configuration"}), 500

    save_config(config)

    # Sync miners to Sentinel so it can monitor any newly added/removed devices
    if "devices" in new_config:
        try:
            synced, sync_errors = sync_miners_to_sentinel()
            if sync_errors:
                app.logger.warning(f"Sentinel sync warnings: {sync_errors}")
        except Exception as e:
            app.logger.error(f"Failed to sync miners to Sentinel: {e}")

        # Invalidate miner cache so next dashboard load fetches fresh data immediately
        miner_cache["last_update"] = 0

    changed_keys = [k for k in new_config if k in allowed_keys]
    record_activity("config", f"Configuration updated: {', '.join(changed_keys)}", {"keys": changed_keys})

    # Return success without exposing full config
    return jsonify({
        "success": True,
        "requires_restart": requires_restart,
        "pool_mode": config.get("pool_mode"),
        "coins": [{"symbol": c.get("symbol")} for c in config.get("coins", [])]
    })


def save_pool_coin_config(coins, multi_coin_enabled):
    """Save coin configuration to the pool's config.yaml file"""
    # Check multiple possible locations for config file
    install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
    config_paths = [
        Path(install_dir) / "config" / "config.yaml",         # Environment-specified location
        Path(__file__).parent.parent.parent / "config" / "config.yaml",  # Relative to src/dashboard
        Path("/spiralpool/config/config.yaml"),               # Common Linux location
        Path("/etc/spiralpool/config.yaml"),                  # Linux system config
        # NOTE: Home dir paths removed — ProtectHome=yes blocks access under systemd
    ]

    # On Windows, add Windows-specific paths
    if os.name == 'nt':
        config_paths.insert(0, Path(os.environ.get("PROGRAMDATA", "C:/ProgramData")) / "SpiralPool" / "config.yaml")
        config_paths.insert(0, Path(os.environ.get("LOCALAPPDATA", "")) / "SpiralPool" / "config.yaml")

    config_path = None
    for path in config_paths:
        try:
            if path.exists():
                config_path = path
                print(f"[CONFIG] Found existing config at: {path}")
                break
        except (PermissionError, OSError):
            continue

    if not config_path:
        # Create default config path — use install dir (systemd-safe), NOT home dir
        # ProtectHome=yes blocks writes to ~/  which would crash mkdir() below
        config_path = Path(install_dir) / "config" / "config.yaml"
        try:
            config_path.parent.mkdir(parents=True, exist_ok=True)
        except (PermissionError, OSError) as e:
            print(f"[CONFIG] WARNING: Could not create config dir {config_path.parent}: {e}")
        print(f"[CONFIG] Creating new config at: {config_path}")

    # Load existing config or create new
    pool_config = {}
    if config_path.exists():
        try:
            with open(config_path) as f:
                pool_config = yaml.safe_load(f) or {}
        except (yaml.YAMLError, IOError):
            pool_config = {}

    # Update coins configuration
    pool_config["version"] = 2 if multi_coin_enabled else 1
    pool_config["coins"] = []

    # Alphabetical order (no coin preference)
    default_ports = {"BC2": 6333, "BCH": 5333, "BTC": 4333, "CAT": 12335,
                     "DGB": 3333, "DGB-SCRYPT": 3336, "DOGE": 8335,
                     "FBTC": 18335, "LTC": 7333, "NMC": 14335,
                     "PEP": 10335, "QBX": 20335, "SYS": 15335, "XMY": 17335}
    default_rpc_ports = {"BC2": 8339, "BCH": 8432, "BTC": 8332, "CAT": 9932,
                         "DGB": 14022, "DGB-SCRYPT": 14022, "DOGE": 22555,
                         "FBTC": 8340, "LTC": 9332, "NMC": 8336,
                         "PEP": 33873, "QBX": 8344, "SYS": 8370, "XMY": 10889}

    # Scrypt coins need different algorithm suffix
    scrypt_coins = {"LTC", "DOGE", "DGB-SCRYPT", "PEP", "CAT"}

    for coin_config in coins:
        symbol = coin_config.get("symbol", "").upper()
        if not symbol or symbol not in default_ports:
            # Skip invalid coins - don't assume DGB
            continue

        # Generate pool_id with underscores (required by PostgreSQL identifier rules)
        # Use algorithm suffix based on coin type
        algo_suffix = "scrypt" if symbol in scrypt_coins else "sha256"
        # Convert symbol to valid pool_id (e.g., DGB-SCRYPT -> dgb_scrypt)
        pool_id_symbol = symbol.lower().replace("-", "_")
        pool_id = f"{pool_id_symbol}_{algo_suffix}" if algo_suffix not in pool_id_symbol else pool_id_symbol

        pool_config["coins"].append({
            "symbol": symbol,
            "enabled": coin_config.get("enabled", True),
            "address": coin_config.get("wallet_address", ""),
            "pool_id": pool_id,
            "stratum": {
                "port": default_ports[symbol]  # Use coin-specific port, no fallback
            },
            "daemon": {
                "port": default_rpc_ports[symbol]  # Use coin-specific port, no fallback
            }
        })

    # Write config atomically (temp + fsync + rename) to prevent corruption on crash
    try:
        import tempfile
        dir_name = os.path.dirname(config_path)
        fd, tmp_path = tempfile.mkstemp(dir=dir_name, suffix='.tmp', prefix='.config_')
        try:
            with os.fdopen(fd, 'w') as f:
                yaml.dump(pool_config, f, default_flow_style=False, sort_keys=False)
                f.flush()
                os.fsync(f.fileno())
            os.replace(tmp_path, config_path)
        except BaseException:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass
            raise
        print(f"[CONFIG] Successfully saved pool config to: {config_path}")
    except PermissionError as e:
        error_msg = f"Permission denied writing to {config_path}. Check file/directory permissions."
        print(f"[CONFIG] Error: {error_msg}")
        raise PermissionError(error_msg) from e
    except OSError as e:
        error_msg = f"Failed to write config file {config_path}: {e}"
        print(f"[CONFIG] Error: {error_msg}")
        raise OSError(error_msg) from e


@app.route('/api/services/restart', methods=['POST'])
@admin_required
def restart_services():
    """Restart pool services (requires appropriate permissions)"""
    import subprocess

    # Strict rate limiting for restart endpoint (max 3 per minute)
    client_ip = request.remote_addr or "unknown"
    if not check_rate_limit(client_ip, "restart"):
        return jsonify({"success": False, "error": "Rate limit exceeded. Please wait before trying again."}), 429

    try:
        # Try systemctl restart (Linux with systemd)
        # Using explicit list of allowed services (no user input in command)
        result = subprocess.run(
            ["sudo", "systemctl", "restart", "spiralstratum", "spiralsentinel"],
            capture_output=True,
            text=True,
            timeout=30
        )
        if result.returncode == 0:
            app.logger.info(f"Services restarted by {client_ip}")
            record_activity("restart", "Services restarted (stratum + sentinel)")
            return jsonify({"success": True, "message": "Services restarted successfully"})
        else:
            app.logger.warning(f"Service restart failed for {client_ip}")
            return jsonify({"success": False, "error": "Service restart failed. Check system logs."})
    except subprocess.TimeoutExpired:
        return jsonify({"success": False, "error": "Restart timed out"})
    except FileNotFoundError:
        # systemctl not available (maybe Docker or different OS)
        return jsonify({"success": False, "error": "Manual restart required (systemctl not available)"})
    except PermissionError:
        return jsonify({"success": False, "error": "Permission denied. Manual restart required."})


@app.route('/api/miners', methods=['GET'])
@api_key_or_login_required
def get_miners():
    """Get all miner data"""
    try:
        # HA BACKUP SAFETY NET: Don't trigger miner polling on BACKUP nodes
        is_backup = ha_status_cache.get("enabled") and ha_status_cache.get("local_role") not in ("MASTER", "STANDALONE", "UNKNOWN")
        if not is_backup and time.time() - miner_cache["last_update"] > 90:
            fetch_all_miners()

        # Get current block reward info
        reward_info = fetch_block_reward()

        # Get pool stats for network difficulty
        # This will verify Spiral Stratum and return zeros if wrong pool
        pool_stats = fetch_pool_stats()

        # Check if we're connected to the wrong pool
        if pool_stats.get("status") == "wrong_pool":
            pool_stats = {
                "network_difficulty": 0,
                "last_block_finder": None,
                "last_block_height": None,
                "pool_hashrate": 0,
                "connected_miners": 0,
                "shares_per_second": 0
            }

        # Get active coin info
        coins = get_enabled_coins()

        # Handle None coin safely - don't default to any specific coin
        primary_coin = coins.get("primary")
        coin_name = MULTI_COIN_NODES.get(primary_coin, {}).get("name", primary_coin) if primary_coin else "Unknown"

        # Get pool share counts from Prometheus metrics (authoritative source)
        prometheus_metrics = fetch_prometheus_metrics()
        pool_accepted = int(prometheus_metrics.get("stratum_shares_accepted_total", 0))
        pool_rejected = int(prometheus_metrics.get("stratum_shares_rejected_total", 0))
        _prom_blocks = int(prometheus_metrics.get("stratum_blocks_found_total", 0))
        _db_blocks = pool_stats.get("blocks_found", -1)
        pool_blocks = max(_prom_blocks, _db_blocks if _db_blocks >= 0 else 0)
        pool_best_diff = prometheus_metrics.get("stratum_best_share_difficulty", 0)

        # H-3 fix: Include wrong_pool status in API response for UI indicator
        is_wrong_pool = pool_stats.get("status") == "wrong_pool"

        # Build merge mining map: {parent_symbol: [aux_symbols]} for enabled parent coins
        def _get_merge_mining_map(enabled_coins):
            result = {}
            for symbol in enabled_coins:
                node = MULTI_COIN_NODES.get(symbol, {})
                mm = node.get("merge_mining") or {}
                if mm.get("role") == "parent" and mm.get("aux_chains"):
                    result[symbol] = mm["aux_chains"]
            return result

        # Build device grouping metadata for frontend grouped view
        # Algorithm is detected per-miner from the stratum port they're connected to
        device_groups = {}
        for miner_name, miner_data in miner_cache["miners"].items():
            algo_info = get_miner_algorithm(miner_data)
            group = get_device_group(miner_data.get("type", ""), algo_info=algo_info)
            if group not in device_groups:
                device_groups[group] = {
                    "name": group,
                    "count": 0,
                    "online_count": 0,
                    "hashrate_ths": 0,
                    "power_watts": 0,
                    "miners": [],
                    "order": DEVICE_GROUP_ORDER.get(group, 99),
                    "default_algorithm": get_device_group_algorithm(group),
                }
            device_groups[group]["count"] += 1
            device_groups[group]["miners"].append(miner_name)
            if miner_data.get("online"):
                device_groups[group]["online_count"] += 1
                hr_ths = miner_data.get("hashrate_ths", 0)
                if not hr_ths:
                    hr_ghs = miner_data.get("hashrate_ghs", 0)
                    hr_ths = hr_ghs / 1000 if hr_ghs else 0
                device_groups[group]["hashrate_ths"] += hr_ths
                power = miner_data.get("power_watts")
                if power is not None:
                    device_groups[group]["power_watts"] += power

        # Compute network hashrate for Statistics chart grid
        _net_diff = pool_stats.get("network_difficulty", 0)
        _net_hashrate = _compute_network_hashrate(_net_diff)

        return jsonify({
            "miners": miner_cache["miners"],
            "totals": miner_cache["totals"],
            "lifetime": lifetime_stats,
            "block_reward": reward_info,
            "network_difficulty": _net_diff,
            "network_hashrate": _net_hashrate,
            "last_block_finder": pool_stats.get("last_block_finder"),
            "last_block_height": pool_stats.get("last_block_height"),
            "last_block_time": pool_stats.get("last_block_time"),
            "last_update": miner_cache["last_update"],
            # Pool hashrate from stratum server (workers connected to THIS pool)
            "pool_hashrate": pool_stats.get("pool_hashrate", 0),
            "pool_connected_miners": pool_stats.get("connected_miners", 0),
            "pool_shares_per_second": pool_stats.get("shares_per_second", 0),
            # Pool share counts from Prometheus (authoritative - actual pool stats, not miner-reported)
            "pool_accepted_shares": pool_accepted,
            "pool_rejected_shares": pool_rejected,
            "pool_blocks_found": pool_blocks,
            "pool_best_share_diff": pool_best_diff,
            # Multi-coin support
            "coin": primary_coin,
            "coin_name": coin_name,
            "enabled_coins": coins.get("enabled", []),
            # Device grouping for frontend grouped view
            "device_groups": device_groups,
            "algorithm": get_algorithm_for_coin(primary_coin) if primary_coin else "sha256d",
            "coins": {
                "primary": primary_coin,
                "enabled": coins.get("enabled", []),
                "multi_coin_mode": coins.get("multi_coin_mode", False),
                "merge_mining": _get_merge_mining_map(coins.get("enabled", [])),
            },
            # H-3: Wrong pool detection for UI warning
            "wrong_pool_detected": is_wrong_pool,
            "pool_status": "wrong_pool" if is_wrong_pool else "ok",
            # Quiet hours: suppress browser celebration (confetti/audio) but not text alerts
            "celebration_quiet_hours": _is_celebration_quiet_hours(),
        })
    except Exception as e:
        # Log the error and return 500 with error info — NOT 200 with zeros
        # Returning 200 with zeros makes the dashboard show false "0 TH/s" instead of an error state
        app.logger.error(f"Error in /api/miners: {e}", exc_info=True)
        # Preserve last-known pool stats for earnings calculator continuity
        cached = pool_stats_cache
        return jsonify({
            "miners": {},
            "totals": {"hashrate_ths": 0, "power_watts": 0, "accepted_shares": 0, "rejected_shares": 0, "blocks_found": 0, "online_count": 0, "total_count": 0},
            "lifetime": lifetime_stats,
            "block_reward": block_reward_cache,
            "network_difficulty": cached.get("network_difficulty", 0),
            "network_hashrate": _compute_network_hashrate(cached.get("network_difficulty", 0)),
            "last_block_finder": cached.get("last_block_finder"),
            "last_block_height": cached.get("last_block_height"),
            "last_block_time": cached.get("last_block_time"),
            "last_update": time.time(),
            "pool_hashrate": cached.get("pool_hashrate", 0),
            "pool_connected_miners": 0,
            "pool_shares_per_second": 0,
            "pool_accepted_shares": 0,
            "pool_rejected_shares": 0,
            "pool_blocks_found": cached.get("blocks_found", 0),
            "pool_best_share_diff": 0,
            "coin": None,
            "coin_name": "Unknown",
            "enabled_coins": [],
            "error": str(e)
        }), 500


@app.route('/api/miners/refresh', methods=['POST'])
@api_key_or_login_required
def refresh_miners():
    """Force refresh miner data"""
    try:
        miners, totals = fetch_all_miners()
        reward_info = fetch_block_reward()
        pool_stats = fetch_pool_stats()
        coins = get_enabled_coins()

        # Handle None coin safely - don't default to any specific coin
        primary_coin = coins.get("primary")
        coin_name = MULTI_COIN_NODES.get(primary_coin, {}).get("name", primary_coin) if primary_coin else "Unknown"

        return jsonify({
            "miners": miners,
            "totals": totals,
            "lifetime": lifetime_stats,
            "block_reward": reward_info,
            "network_difficulty": pool_stats.get("network_difficulty", 0),
            "network_hashrate": _compute_network_hashrate(pool_stats.get("network_difficulty", 0)),
            "last_block_finder": pool_stats.get("last_block_finder"),
            "last_block_height": pool_stats.get("last_block_height"),
            "last_block_time": pool_stats.get("last_block_time"),
            "last_update": miner_cache["last_update"],
            # Pool hashrate from stratum server (workers connected to THIS pool)
            "pool_hashrate": pool_stats.get("pool_hashrate", 0),
            "pool_connected_miners": pool_stats.get("connected_miners", 0),
            "pool_shares_per_second": pool_stats.get("shares_per_second", 0),
            # Multi-coin support
            "coin": primary_coin,
            "coin_name": coin_name,
            "enabled_coins": coins.get("enabled", [])
        })
    except Exception as e:
        app.logger.error(f"Error in /api/miners/refresh: {e}", exc_info=True)
        return jsonify({
            "miners": {},
            "totals": {"hashrate_ths": 0, "power_watts": 0, "accepted_shares": 0, "rejected_shares": 0, "blocks_found": 0, "online_count": 0, "total_count": 0},
            "lifetime": lifetime_stats,
            "block_reward": block_reward_cache,
            "network_difficulty": 0,
            "last_block_finder": None,
            "last_block_height": None,
            "last_block_time": None,
            "last_update": time.time(),
            "pool_hashrate": 0,
            "pool_connected_miners": 0,
            "pool_shares_per_second": 0,
            "coin": "unknown",
            "coin_name": "Unknown",
            "enabled_coins": [],
            "error": str(e)
        })


@app.route('/api/stats/reset', methods=['POST'])
@admin_required
def reset_stats():
    """Reset lifetime statistics to zero"""
    global lifetime_stats

    try:
        with _lifetime_stats_lock:
            lifetime_stats = {
                "total_shares": 0,
                "total_pool_shares": 0,
                "total_blocks": 0,
                "best_share_difficulty": 0,
                "uptime_start": time.time(),
                "total_runtime_seconds": 0
            }
            save_stats()

        return jsonify({"success": True, "message": "Lifetime stats have been reset"})
    except Exception as e:
        app.logger.error(f"Lifetime stats reset error: {e}")
        return jsonify({"success": False, "error": "Failed to reset lifetime stats"}), 500


@app.route('/api/miners/check-sentinel-db', methods=['GET'])
@admin_required
def api_check_sentinel_db():
    """Check if miners.json exists and has miners available to import.

    Used by setup page to prompt users if pre-scanned miners exist.
    Returns count of available miners and path where found.
    """
    install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
    possible_paths = [
        Path(install_dir) / "data" / "miners.json",
        Path(__file__).parent.parent.parent / "data" / "miners.json",
        Path("/spiralpool/data/miners.json"),
        # NOTE: Home dir path removed — ProtectHome=yes blocks access under systemd
    ]

    if os.name == 'nt':
        possible_paths.insert(0, Path(os.environ.get("PROGRAMDATA", "C:/ProgramData")) / "SpiralPool" / "miners.json")
        possible_paths.insert(0, Path(os.environ.get("LOCALAPPDATA", "")) / "SpiralPool" / "miners.json")

    for path in possible_paths:
        try:
            if path.exists():
                with open(path, 'r') as f:
                    data = json.load(f)
                miners = data.get("miners", {})
                if miners and isinstance(miners, dict):
                    # Check how many are NOT already in dashboard config
                    config = load_config()
                    # Collect actual IPs from all device type lists
                    existing_ips = set()
                    for device_type, devices in config.get("devices", {}).items():
                        if isinstance(devices, list):
                            for device in devices:
                                if isinstance(device, dict) and device.get("ip"):
                                    existing_ips.add(device["ip"])
                    new_miners = {ip: m for ip, m in miners.items() if ip not in existing_ips}
                    return jsonify({
                        "available": True,
                        "total_in_db": len(miners),
                        "new_count": len(new_miners),
                        "already_imported": len(miners) - len(new_miners),
                        "path": str(path)
                    })
        except (PermissionError, OSError, json.JSONDecodeError):
            continue

    return jsonify({
        "available": False,
        "total_in_db": 0,
        "new_count": 0,
        "already_imported": 0,
        "path": None
    })


@app.route('/api/miners/import-from-sentinel', methods=['POST'])
@admin_required
def api_import_from_sentinel():
    """Import miners from the spiralpool-scan database into dashboard configuration.

    This allows miners discovered by the install script's scanner to be
    automatically added to the dashboard without manual re-entry.

    Returns:
        JSON with imported, skipped counts and any errors
    """
    print("[API] import-from-sentinel endpoint called")
    try:
        imported, skipped, errors = import_miners_from_sentinel()
        print(f"[API] Import result: imported={imported}, skipped={skipped}, errors={errors}")

        # After importing, sync back to Sentinel to ensure consistency
        # This handles the case where import adds new miners that Sentinel should know about
        if imported > 0:
            try:
                sync_miners_to_sentinel()
                print(f"[API] Synced {imported} miners to Sentinel")
            except Exception as sync_err:
                print(f"[SYNC] Warning: Could not sync to Sentinel after import: {sync_err}")

        return jsonify({
            "success": True,
            "imported": imported,
            "skipped": skipped,
            "errors": errors,
            "message": f"Imported {imported} miners, skipped {skipped} duplicates"
        })
    except Exception as e:
        import traceback
        print(f"[API] Import exception: {e}")
        traceback.print_exc()
        return jsonify({
            "success": False,
            "error": str(e),
            "imported": 0,
            "skipped": 0
        }), 500


@app.route('/api/miners/sync-to-sentinel', methods=['POST'])
@admin_required
def api_sync_to_sentinel():
    """Sync all dashboard miners to Sentinel's unified database.

    This ensures that miners configured in the dashboard are visible to
    Spiral Sentinel for monitoring and Discord notifications.

    Returns:
        JSON with sync count and any errors
    """
    try:
        synced, errors = sync_miners_to_sentinel()
        return jsonify({
            "success": True,
            "synced": synced,
            "errors": errors,
            "message": f"Synced {synced} miners to Sentinel database"
        })
    except Exception as e:
        return jsonify({
            "success": False,
            "error": str(e),
            "synced": 0
        }), 500


@app.route('/api/miner/<name>/history', methods=['GET'])
@api_key_or_login_required
def get_miner_history(name):
    """Get hashrate history for a specific miner/worker.

    Serves local per-miner hashrate data collected by the dashboard.
    Falls back to stratum server's worker history endpoint if no local data.

    Query params:
        hours: Time range in hours (1-720, default 24)

    Returns:
        List of {timestamp, hashrate} objects
    """
    # SECURITY: Validate and sanitize worker name
    if not name or len(name) > 128:
        return jsonify({"error": "Invalid worker name"}), 400

    # Allow spaces in name (e.g., "Cardy ESP32 Miner") but sanitize for safety
    import re
    if not re.match(r'^[a-zA-Z0-9._ -]+$', name):
        return jsonify({"error": "Invalid worker name format"}), 400

    hours = request.args.get('hours', 24, type=int)
    hours = max(1, min(720, hours))  # Clamp to valid range

    # --- Try local per-miner history first ---
    cutoff = time.time() - (hours * 3600)
    local_deque = historical_data["per_miner_hashrate"].get(name)

    if local_deque and len(local_deque) > 0:
        result = []
        for point in local_deque:
            if point["time"] >= cutoff:
                result.append({
                    "timestamp": int(point["time"] * 1000),  # JS milliseconds
                    "hashrate": point["value"] * 1e12         # TH/s → H/s
                })
        if result:
            return jsonify(result)

    # --- Fallback: stratum server worker history ---
    miner_address = None
    if miner_cache["miners"]:
        if "." in name:
            miner_address = name.split(".")[0]
        else:
            config = load_config()
            miner_address = config.get("pool_address", "")

    if not miner_address:
        return jsonify([])

    # SECURITY: Sanitize miner_address for URL construction
    if not re.match(r'^[a-zA-Z0-9]+$', miner_address):
        return jsonify({"error": "Invalid miner address format"}), 400

    try:
        from urllib.parse import quote
        safe_name = quote(name, safe='')
        safe_address = quote(miner_address, safe='')
        url = f"{POOL_API_URL}/api/pools/{get_pool_id()}/miners/{safe_address}/workers/{safe_name}/history"
        resp = requests.get(url, params={"hours": hours}, timeout=10)

        if resp.status_code == 200:
            return jsonify(resp.json())
        else:
            app.logger.warning(f"Worker history not found: {name} ({resp.status_code})")
            return jsonify([])

    except requests.exceptions.Timeout:
        app.logger.error(f"Timeout fetching worker history for {name}")
        return jsonify({"error": "Request timeout", "data": []}), 504
    except requests.exceptions.RequestException as e:
        app.logger.error(f"Error fetching worker history: {e}")
        return jsonify({"error": "Failed to fetch worker history", "data": []}), 500


@app.route('/api/miner/<name>/stats', methods=['GET'])
@api_key_or_login_required
def get_miner_stats(name):
    """Get detailed stats for a specific miner/worker.

    Returns extended statistics including multi-window hashrates
    and acceptance rates from the stratum server.

    Handles mismatch between dashboard display name and stratum worker name
    by looking up the stratum worker name via IP if the direct lookup fails.
    """
    # SECURITY: Validate and sanitize worker name
    import re
    if not name or len(name) > 128:
        return jsonify({"error": "Invalid worker name"}), 400

    # Allow spaces in name (e.g., "Cardy ESP32 Miner") but sanitize for safety
    if not re.match(r'^[a-zA-Z0-9._ -]+$', name):
        return jsonify({"error": "Invalid worker name format"}), 400

    # Get miner address from config
    config = load_config()
    miner_address = config.get("pool_address", "")

    # If name contains ".", first part might be the address
    if "." in name:
        potential_address = name.split(".")[0]
        # Only use if it looks like a valid address (alphanumeric, reasonable length)
        if re.match(r'^[a-zA-Z0-9]{26,64}$', potential_address):
            miner_address = potential_address

    if not miner_address:
        return jsonify({"error": "Miner address not found - configure pool_address in settings"}), 404

    # SECURITY: Validate miner address format
    if not re.match(r'^[a-zA-Z0-9]+$', miner_address):
        return jsonify({"error": "Invalid miner address format"}), 400

    # Get device data from cache
    device_data = miner_cache["miners"].get(name, {})

    # Determine the worker name to query the pool with
    # Priority: 1) stratum worker name from IP mapping, 2) display name
    worker_name = name
    stratum_worker = None

    if device_data and device_data.get("ip"):
        # Look up the stratum worker name for this device's IP
        ip = device_data["ip"]
        stratum_worker = get_worker_name_for_ip(ip, None)
        if stratum_worker and stratum_worker != ip:
            worker_name = stratum_worker

    try:
        # Fetch detailed worker stats from stratum server
        from urllib.parse import quote
        safe_name = quote(worker_name, safe='')
        safe_address = quote(miner_address, safe='')
        url = f"{POOL_API_URL}/api/pools/{get_pool_id()}/miners/{safe_address}/workers/{safe_name}"
        resp = requests.get(url, timeout=10)

        worker_data = None
        if resp.status_code == 200:
            worker_data = resp.json()
        elif stratum_worker and stratum_worker != name:
            # If we tried with stratum worker name and failed, try with display name
            safe_display_name = quote(name, safe='')
            url2 = f"{POOL_API_URL}/api/pools/{get_pool_id()}/miners/{safe_address}/workers/{safe_display_name}"
            resp2 = requests.get(url2, timeout=10)
            if resp2.status_code == 200:
                worker_data = resp2.json()

        if worker_data:
            return jsonify({
                "worker": worker_data,
                "device": device_data,
                "combined": {
                    **device_data,
                    "hashrates": worker_data.get("hashrates", {}),
                    "shares_submitted": worker_data.get("sharesSubmitted", 0),
                    "shares_accepted": worker_data.get("sharesAccepted", 0),
                    "shares_rejected": worker_data.get("sharesRejected", 0),
                    "acceptance_rate": worker_data.get("acceptanceRate", 100),
                    "current_difficulty": worker_data.get("difficulty", 0),
                    "current_hashrate": worker_data.get("currentHashrate", 0),
                    "average_hashrate": worker_data.get("averageHashrate", 0),
                    "connected": worker_data.get("connected", False),
                }
            })
        else:
            # Fall back to cached data only
            if device_data:
                return jsonify({"device": device_data, "worker": None})
            return jsonify({"error": "Miner not found"}), 404

    except requests.exceptions.RequestException as e:
        # SECURITY: Log full error but return generic message to avoid info disclosure
        app.logger.error(f"Error fetching worker stats for {name}: {e}")
        # Fall back to cached data on error
        if device_data:
            return jsonify({"device": device_data, "worker": None, "error": "Failed to fetch live stats"})
        return jsonify({"error": "Failed to fetch worker stats"}), 500


@app.route('/api/device/test', methods=['POST'])
@api_key_or_login_required
def test_device():
    """Test connection to a device"""
    data = request.json
    device_type = data.get("type", "axeos")
    ip = data.get("ip")
    port = data.get("port", 4028)

    if not ip:
        return jsonify({"success": False, "error": "IP address required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    # AxeOS-based devices (BitAxe, NerdQAxe, NMaxe, QAxe, Hammer, Lucky Miner, Jingle, Zyber, ESP32)
    if device_type in ["axeos", "nerdqaxe", "nmaxe", "qaxe", "qaxeplus", "esp32miner", "hammer", "luckyminer", "jingleminer", "zyber"]:
        result = fetch_axeos(ip, timeout=3)
    # BraiinsOS devices
    elif device_type == "braiins":
        result = fetch_braiins(ip, timeout=3)
    # Vnish firmware devices
    elif device_type == "vnish":
        result = fetch_vnish(ip, timeout=3)
    # LuxOS firmware devices
    elif device_type == "luxos":
        result = fetch_luxos(ip, port, timeout=3)
    # CGMiner-based devices
    elif device_type == "avalon":
        result = fetch_avalon(ip, port, timeout=3)
    elif device_type in ["antminer", "antminer_scrypt"]:
        result = fetch_antminer(ip, port, timeout=3)
    elif device_type == "whatsminer":
        result = fetch_whatsminer(ip, port, timeout=3)
    elif device_type == "innosilicon":
        result = fetch_innosilicon(ip, port, timeout=3)
    elif device_type == "epic":
        result = fetch_epic_http(ip, port, timeout=3)  # HTTP REST on port 4028
    elif device_type in ["futurebit", "gekkoscience", "ipollo", "ebang", "elphapex", "canaan"]:
        result = fetch_antminer(ip, port, timeout=3)  # CGMiner-compatible (best-effort)
    elif device_type == "goldshell":
        result = fetch_goldshell(ip, timeout=3)
    else:
        return jsonify({"success": False, "error": "Unknown device type"})

    return jsonify({
        "success": result.get("online", False),
        "data": result
    })


@app.route('/api/test/block-celebration', methods=['POST'])
@admin_required
def test_block_celebration():
    """Test the Avalon LED block celebration.

    This endpoint manually triggers the 1-hour LED celebration pattern
    on all configured Avalon miners. Use this to test the celebration
    without actually finding a block.

    POST /api/test/block-celebration
    Optional body: {"duration": 60}  # Override duration in seconds (default: 3600 = 1 hour)

    Returns: {"success": true, "message": "...", "avalon_count": N}
    """
    data = request.json or {}
    duration_override = data.get("duration")  # Optional: override duration in seconds
    force = data.get("force", False)          # Optional: bypass quiet hours check

    # Get Avalon miner count
    config = load_config()
    avalon_devices = config.get("devices", {}).get("avalon", [])

    if not avalon_devices:
        return jsonify({
            "success": False,
            "error": "No Avalon miners configured",
            "message": "Add Avalon miners in Settings > Devices first"
        })

    # Check quiet hours (unless force override)
    if not force and _is_celebration_quiet_hours():
        return jsonify({
            "success": False,
            "error": "Quiet hours active",
            "message": "LED celebration suppressed — efficiency schedule is active. Use {\"force\": true} to override."
        })

    # Trigger the celebration (optionally with custom duration for testing)
    if duration_override and isinstance(duration_override, int) and 1 <= duration_override <= 3600:
        # For testing, allow shorter duration
        trigger_avalon_block_celebration_with_duration(duration_override)
        return jsonify({
            "success": True,
            "message": f"LED celebration triggered for {duration_override} seconds on {len(avalon_devices)} Avalon miner(s)!",
            "avalon_count": len(avalon_devices),
            "duration": duration_override
        })
    else:
        # Full 1-hour celebration
        trigger_avalon_block_celebration()
        return jsonify({
            "success": True,
            "message": f"LED celebration triggered for 1 HOUR on {len(avalon_devices)} Avalon miner(s)! 🎉",
            "avalon_count": len(avalon_devices),
            "duration": 3600
        })


def trigger_avalon_block_celebration_with_duration(duration_seconds):
    """Trigger LED celebration with custom duration (for testing).

    Same as trigger_avalon_block_celebration() but with configurable duration.
    Respects _celebration_cancel event to prevent thread explosion.
    """
    global _celebration_active
    import random

    # Cancel any active celebration before starting
    _celebration_cancel.set()
    time.sleep(0.2)
    _celebration_cancel.clear()
    _celebration_active = True

    def led_on(ip, port):
        cgminer_command(ip, port, "ascset", "0,led,1", timeout=2)

    def led_off(ip, port):
        cgminer_command(ip, port, "ascset", "0,led,0", timeout=2)

    def celebration_sequence(ip, port, duration):
        try:
            start_time = time.time()
            end_time = start_time + duration
            pattern_num = 0

            while time.time() < end_time and not _celebration_cancel.is_set():
                pattern_num = (pattern_num + 1) % 10

                if pattern_num == 0:
                    # RAVE MODE
                    for _ in range(20):
                        led_on(ip, port)
                        time.sleep(0.05)
                        led_off(ip, port)
                        time.sleep(0.05)
                elif pattern_num == 1:
                    # Heartbeat
                    for _ in range(4):
                        led_on(ip, port)
                        time.sleep(0.1)
                        led_off(ip, port)
                        time.sleep(0.1)
                        led_on(ip, port)
                        time.sleep(0.1)
                        led_off(ip, port)
                        time.sleep(0.6)
                elif pattern_num == 2:
                    # Slow pulse
                    for _ in range(3):
                        led_on(ip, port)
                        time.sleep(1.5)
                        led_off(ip, port)
                        time.sleep(0.5)
                elif pattern_num == 3:
                    # Morse "BLOCK"
                    morse_block = [
                        [0.4, 0.1, 0.1, 0.1],
                        [0.1, 0.4, 0.1, 0.1],
                        [0.4, 0.4, 0.4],
                        [0.4, 0.1, 0.4, 0.1],
                        [0.4, 0.1, 0.4],
                    ]
                    for letter in morse_block:
                        for dur in letter:
                            led_on(ip, port)
                            time.sleep(dur)
                            led_off(ip, port)
                            time.sleep(0.1)
                        time.sleep(0.3)
                elif pattern_num == 4:
                    # Accelerating
                    delays = [0.5, 0.4, 0.3, 0.2, 0.15, 0.1, 0.08, 0.05]
                    for delay in delays + list(reversed(delays)):
                        led_on(ip, port)
                        time.sleep(delay)
                        led_off(ip, port)
                        time.sleep(delay)
                elif pattern_num == 5:
                    # Party mode
                    for _ in range(15):
                        led_on(ip, port)
                        time.sleep(random.uniform(0.05, 0.3))
                        led_off(ip, port)
                        time.sleep(random.uniform(0.05, 0.2))
                else:
                    # Quick strobe
                    for _ in range(10):
                        led_on(ip, port)
                        time.sleep(0.1)
                        led_off(ip, port)
                        time.sleep(0.1)

                time.sleep(0.5)

            led_off(ip, port)
        except Exception:
            pass
        finally:
            global _celebration_active
            _celebration_active = False

    try:
        config = load_config()
        avalon_devices = config.get("devices", {}).get("avalon", [])

        for device in avalon_devices:
            ip = device.get("ip")
            port = device.get("port", 4028)
            if ip:
                thread = threading.Thread(
                    target=celebration_sequence,
                    args=(ip, port, duration_seconds),
                    daemon=True
                )
                thread.start()
    except Exception:
        pass


@app.route('/settings')
@api_key_or_login_required
def settings():
    """Settings page"""
    config = load_config()
    return render_template('settings.html', config=config)


@app.route('/api/device/restart', methods=['POST'])
@admin_required
def restart_device():
    """Restart a miner device (AxeOS-based miners and Avalon ASICs)"""
    data = request.json
    name = data.get("name", "")
    ip = data.get("ip", "")
    device_type = data.get("type", "").lower()

    if not ip:
        return jsonify({"success": False, "error": "IP address required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    try:
        # AxeOS-based miners (BitAxe, NMaxe, NerdQAxe, Hammer, Lucky Miner, Jingle, Zyber) use /api/system/restart
        if (device_type in ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "bitaxe", "hammer", "luckyminer", "jingleminer", "zyber"]
                or "axe" in device_type.lower()
                or device_type.startswith("qaxe")
                or device_type.startswith("lucky miner")
                or device_type.startswith("jingle miner")
                or device_type.startswith("zyber")):
            response = requests.post(f"http://{ip}/api/system/restart", timeout=5)
            if response.status_code == 200:
                return jsonify({"success": True, "message": f"Restart command sent to {name}"})
            else:
                return jsonify({"success": False, "error": f"Device returned status {response.status_code}"})
        # CGMiner-based devices use CGMiner API restart command
        elif device_type in ["antminer", "antminer_scrypt", "whatsminer", "innosilicon", "futurebit", "gekkoscience", "ipollo", "ebang", "epic", "elphapex"]:
            result = cgminer_command(ip, 4028, "restart", timeout=5)
            if "error" not in result:
                return jsonify({"success": True, "message": f"Restart command sent to {name}"})
            else:
                return jsonify({"success": False, "error": result.get("error", "CGMiner restart failed")})
        # Goldshell uses HTTP API
        elif device_type == "goldshell":
            for endpoint in ["/mcb/restart", "/mcb/reboot", "/restart", "/reboot"]:
                try:
                    response = requests.post(f"http://{ip}{endpoint}", timeout=5)
                    if response.status_code == 200:
                        return jsonify({"success": True, "message": f"Restart command sent to {name}"})
                except Exception:
                    continue
            return jsonify({"success": False, "error": "Goldshell restart failed - no working endpoint found"})
        # BraiinsOS uses REST API for reboot
        elif device_type == "braiins" or "braiins" in device_type.lower() or "bos" in device_type.lower():
            # Get credentials from config if available
            config = load_config()
            device_config = None
            for dev in config.get("devices", {}).get("braiins", []):
                if dev.get("ip") == ip:
                    device_config = dev
                    break
            username = device_config.get("username", "root") if device_config else "root"
            password = device_config.get("password", "") if device_config else ""
            result = braiins_api_call(ip, "/actions/reboot", method="PUT", username=username, password=password, timeout=10)
            if result.get("error"):
                return jsonify({"success": False, "error": result.get("error")})
            return jsonify({"success": True, "message": f"Reboot command sent to {name}"})
        # Vnish uses REST API for reboot
        elif device_type == "vnish" or "vnish" in device_type.lower():
            config = load_config()
            device_config = None
            for dev in config.get("devices", {}).get("vnish", []):
                if dev.get("ip") == ip:
                    device_config = dev
                    break
            password = device_config.get("password", "admin") if device_config else "admin"
            result = vnish_api_call(ip, "/api/v1/system/reboot", method="POST", password=password, timeout=10)
            if result.get("error"):
                return jsonify({"success": False, "error": result.get("error")})
            return jsonify({"success": True, "message": f"Reboot command sent to {name}"})
        # LuxOS uses CGMiner-compatible API
        elif device_type == "luxos" or "luxos" in device_type.lower():
            result = luxos_command(ip, 4028, "restart", timeout=5)
            if "error" not in result:
                return jsonify({"success": True, "message": f"Restart command sent to {name}"})
            else:
                return jsonify({"success": False, "error": result.get("error", "LuxOS restart failed")})
        else:
            return jsonify({"success": False, "error": f"Restart not supported for {device_type}"})
    except requests.exceptions.Timeout:
        # Timeout is expected as the device restarts
        return jsonify({"success": True, "message": f"Restart command sent to {name}"})
    except Exception as e:
        app.logger.error(f"Device restart error for {name}: {e}")
        return jsonify({"success": False, "error": "Failed to restart device"})


@app.route('/api/device/frequency', methods=['POST'])
@admin_required
def update_device_frequency():
    """Update miner frequency (AxeOS-based miners only)"""
    data = request.json
    name = data.get("name", "")
    ip = data.get("ip", "")
    device_type = data.get("type", "").lower()
    frequency = data.get("frequency")

    if not ip:
        return jsonify({"success": False, "error": "IP address required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    if not frequency:
        return jsonify({"success": False, "error": "Frequency required"})

    try:
        freq = int(frequency)
        if freq < 100 or freq > 1000:
            return jsonify({"success": False, "error": "Frequency must be between 100-1000 MHz"})
    except ValueError:
        return jsonify({"success": False, "error": "Invalid frequency value"})

    try:
        # AxeOS-based miners use PATCH /api/system with frequency parameter
        if (device_type in ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "bitaxe", "hammer", "luckyminer", "jingleminer", "zyber"]
                or "axe" in device_type.lower()
                or device_type.startswith("qaxe")
                or device_type.startswith("lucky miner")
                or device_type.startswith("jingle miner")
                or device_type.startswith("zyber")):
            # First get current settings
            get_resp = requests.get(f"http://{ip}/api/system", timeout=5)
            if get_resp.status_code != 200:
                return jsonify({"success": False, "error": "Failed to get current settings"})

            current_settings = get_resp.json()

            # Update frequency
            update_data = {
                "frequency": freq
            }

            response = requests.patch(
                f"http://{ip}/api/system",
                json=update_data,
                timeout=5
            )

            if response.status_code == 200:
                # Trigger restart to apply
                try:
                    requests.post(f"http://{ip}/api/system/restart", timeout=2)
                except requests.exceptions.RequestException:
                    pass  # Restart may timeout, that's ok
                return jsonify({"success": True, "message": f"Frequency updated to {freq} MHz"})
            else:
                return jsonify({"success": False, "error": f"Device returned status {response.status_code}"})
        else:
            return jsonify({"success": False, "error": f"Frequency update not supported for {device_type}"})
    except Exception as e:
        app.logger.error(f"Device frequency update error: {e}")
        return jsonify({"success": False, "error": "Failed to update device frequency"})


@app.route('/api/device/overclock', methods=['POST'])
@admin_required
def overclock_device():
    """
    Apply overclock settings (frequency and voltage) to AxeOS-based miners and Avalon ASICs.

    WARNING: Overclocking can cause hardware damage. Users must accept
    responsibility before using this feature.
    """
    data = request.json
    name = data.get("name", "")
    ip = data.get("ip", "")
    device_type = data.get("type", "").lower()
    frequency = data.get("frequency")
    voltage = data.get("voltage")

    if not ip:
        return jsonify({"success": False, "error": "IP address required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    if not frequency and not voltage:
        return jsonify({"success": False, "error": "Frequency or voltage required"})

    # Validate frequency
    freq = None
    if frequency:
        try:
            freq = int(frequency)
            if freq < 100 or freq > 1200:
                return jsonify({"success": False, "error": "Frequency must be between 100-1200 MHz"})
        except ValueError:
            return jsonify({"success": False, "error": "Invalid frequency value"})

    # Validate voltage (in mV)
    volt = None
    if voltage:
        try:
            volt = int(voltage)
            if volt < 1000 or volt > 1400:
                return jsonify({"success": False, "error": "Voltage must be between 1000-1400 mV"})
        except ValueError:
            return jsonify({"success": False, "error": "Invalid voltage value"})

    try:
        # AxeOS-based miners use PATCH /api/system
        if (device_type in ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "bitaxe", "hammer", "luckyminer", "jingleminer", "zyber"]
                or "axe" in device_type.lower()
                or device_type.startswith("qaxe")
                or device_type.startswith("lucky miner")
                or device_type.startswith("jingle miner")
                or device_type.startswith("zyber")):
            # Get current settings first
            get_resp = requests.get(f"http://{ip}/api/system", timeout=5)
            if get_resp.status_code != 200:
                return jsonify({"success": False, "error": "Failed to get current settings"})

            current_settings = get_resp.json()

            # Build update payload
            update_data = {}
            if freq is not None:
                update_data["frequency"] = freq
            if volt is not None:
                # AxeOS uses "coreVoltage" for voltage setting (in mV)
                update_data["coreVoltage"] = volt

            if not update_data:
                return jsonify({"success": False, "error": "No valid settings to update"})

            # Apply settings
            response = requests.patch(
                f"http://{ip}/api/system",
                json=update_data,
                timeout=5
            )

            if response.status_code == 200:
                # Trigger restart to apply new settings
                try:
                    requests.post(f"http://{ip}/api/system/restart", timeout=2)
                except requests.exceptions.RequestException:
                    pass  # Restart may timeout, that's expected

                result_msg = f"Overclock applied to {name}: "
                if freq:
                    result_msg += f"Frequency={freq}MHz "
                if volt:
                    result_msg += f"Voltage={volt}mV"

                return jsonify({"success": True, "message": result_msg.strip()})
            else:
                return jsonify({"success": False, "error": f"Device returned status {response.status_code}"})

        # Avalon ASICs use CGMiner API ascset command (EXPERIMENTAL)
        elif "avalon" in device_type.lower():
            result_msg = f"[EXPERIMENTAL] Overclock applied to {name}: "
            errors = []

            # Set frequency via CGMiner ascset command
            # Format: ascset|0,freq,{value} or ascset|0,frequency,{value}
            if freq is not None:
                freq_result = cgminer_command(ip, 4028, "ascset", f"0,freq,{freq}", timeout=5)
                if "error" in freq_result:
                    errors.append(f"Frequency: {freq_result.get('error')}")
                else:
                    result_msg += f"Frequency={freq}MHz "

            # Set voltage via CGMiner ascset command
            # Format: ascset|0,voltage,{value} - Avalon uses different voltage scale
            # Avalon Nano 3 typically uses voltage values like 7750 (not mV directly)
            if volt is not None:
                # Convert mV to Avalon voltage format if needed
                # Avalon uses ~7000-8500 range for voltage (not 1000-1400 mV)
                avalon_volt = volt * 6 if volt < 2000 else volt  # Scale up if in mV range
                volt_result = cgminer_command(ip, 4028, "ascset", f"0,voltage,1-{avalon_volt}", timeout=5)
                if "error" in volt_result:
                    errors.append(f"Voltage: {volt_result.get('error')}")
                else:
                    result_msg += f"Voltage={volt}mV "

            if errors:
                return jsonify({"success": False, "error": f"[EXPERIMENTAL] {'; '.join(errors)}"})

            return jsonify({"success": True, "message": result_msg.strip(), "experimental": True})

        else:
            return jsonify({"success": False, "error": f"Overclock not supported for {device_type}"})
    except requests.exceptions.Timeout:
        # Timeout after PATCH is likely due to restart
        return jsonify({"success": True, "message": f"Overclock settings sent to {name}"})
    except Exception as e:
        app.logger.error(f"Overclock error for {name}: {e}")
        return jsonify({"success": False, "error": "Failed to apply overclock settings"})


@app.route('/api/scan/start', methods=['POST'])
@api_key_or_login_required
def start_scan():
    """Start subnet scan for miners"""
    global scan_progress

    global _scan_thread

    # Log current state for debugging "two clicks needed" issue
    print(f"[SCAN] start_scan called: running={scan_progress.get('running')}, phase={scan_progress.get('phase')}, scanned={scan_progress.get('scanned')}")

    # Check if a scan is currently running
    if scan_progress["running"]:
        start_time = scan_progress.get("start_time", 0)
        elapsed = time.time() - start_time if start_time else 999  # Treat missing start_time as very old
        heartbeat = scan_progress.get("heartbeat", 0)
        heartbeat_age = time.time() - heartbeat if heartbeat else 999

        # Check if thread is actually alive
        thread_alive = _scan_thread is not None and _scan_thread.is_alive()

        # Force reset aggressively to prevent "two clicks needed" bug
        # Users should never have to click twice - reset on any hint of stale state
        should_reset = False
        phase = scan_progress.get("phase", "")

        if not thread_alive:
            print("[SCAN] WARNING: Scan thread died, forcing reset")
            should_reset = True
        elif phase in ("complete", "error", "idle"):
            # Inconsistent state: running=True but phase says we're done
            print(f"[SCAN] WARNING: Inconsistent state (running=True, phase={phase}), forcing reset")
            should_reset = True
        elif elapsed > 150:
            # Max scan time is 2 minutes, allow 30s grace period
            # Only force reset if scan exceeds 2.5 minutes
            print("[SCAN] WARNING: Previous scan stuck (>150s), forcing reset")
            should_reset = True
        elif heartbeat_age > 30 and scan_progress.get("scanned", 0) > 0:
            # Thread hasn't updated progress in 30 seconds but had started scanning
            print(f"[SCAN] WARNING: No heartbeat for {heartbeat_age:.0f}s, forcing reset")
            should_reset = True
        elif elapsed > 15 and scan_progress.get("scanned", 0) == 0:
            # If no progress after 15s, something may be wrong
            # (warmup takes ~2s, first hosts should complete within 10s)
            print("[SCAN] WARNING: Scan stuck with no progress (>15s), forcing reset")
            should_reset = True
        elif start_time == 0:
            # No start time recorded - corrupted state
            print("[SCAN] WARNING: No start_time recorded, forcing reset")
            should_reset = True

        if should_reset:
            scan_progress["running"] = False
            scan_progress["phase"] = "complete"
            scan_progress["phase_msg"] = "Previous scan reset"
            scan_progress["error"] = None
            _scan_thread = None
        else:
            # Scan is legitimately running - return status info so frontend can show progress
            return jsonify({
                "success": False,
                "error": "Scan already in progress",
                "phase": scan_progress.get("phase"),
                "scanned": scan_progress.get("scanned"),
                "total": scan_progress.get("total"),
                "elapsed": round(elapsed, 1)
            })

    # Handle both JSON body and empty requests
    try:
        data = request.get_json(silent=True) or {}
    except Exception:
        data = {}
    subnet = data.get("subnet")  # None = auto-detect

    # CRITICAL: Initialize ALL state BEFORE starting thread to prevent race condition
    # where JavaScript polls status before thread has started and sees stale/incomplete state
    current_time = time.time()
    scan_progress["running"] = True
    scan_progress["scanned"] = 0
    # Set estimated total to 254 (typical /24 subnet) to prevent "stuck at 0" false positive
    # The actual total will be updated once subnet detection completes
    scan_progress["total"] = 254
    scan_progress["found"] = []
    scan_progress["error"] = None
    scan_progress["phase"] = "initializing"
    scan_progress["phase_msg"] = "Detecting network..."
    scan_progress["start_time"] = current_time
    scan_progress["heartbeat"] = current_time  # Initialize heartbeat
    scan_progress["subnet"] = None
    scan_progress["current_ip"] = None

    # Start background scan
    _scan_thread = threading.Thread(target=run_background_scan, args=(subnet,))
    _scan_thread.daemon = True
    _scan_thread.start()

    return jsonify({
        "success": True,
        "message": "Scan started"
    })


@app.route('/api/scan/status', methods=['GET'])
@api_key_or_login_required
def scan_status():
    """Get current scan progress with enhanced status info"""
    # Calculate elapsed time if scan is running
    elapsed = 0
    if scan_progress.get("start_time") and scan_progress.get("running"):
        elapsed = time.time() - scan_progress["start_time"]

    # Return enhanced status
    return jsonify({
        **scan_progress,
        "elapsed_seconds": round(elapsed, 1),
        "elapsed_formatted": f"{int(elapsed)}s" if elapsed < 60 else f"{int(elapsed // 60)}m {int(elapsed % 60)}s"
    })


@app.route('/api/scan/subnet', methods=['GET'])
@api_key_or_login_required
def get_subnet():
    """Get detected subnet info"""
    subnet, local_ip = get_local_subnet()
    return jsonify({
        "subnet": subnet,
        "local_ip": local_ip
    })


# ============================================
# THEME API ROUTES
# ============================================

@app.route('/api/themes', methods=['GET'])
@api_key_or_login_required
def get_themes():
    """Get list of available themes"""
    themes_dir = Path(__file__).parent / 'static' / 'themes'
    themes = []

    if themes_dir.exists():
        for theme_file in themes_dir.glob('*.json'):
            try:
                with open(theme_file, 'r') as f:
                    theme_data = json.load(f)
                    themes.append({
                        'id': theme_data.get('id', theme_file.stem),
                        'name': theme_data.get('name', theme_file.stem),
                        'description': theme_data.get('description', ''),
                        'category': theme_data.get('category', 'Other')
                    })
            except (json.JSONDecodeError, IOError):
                continue

    return jsonify(themes)


@app.route('/api/themes/<theme_id>', methods=['GET'])
@api_key_or_login_required
def get_theme(theme_id):
    """Get a specific theme's full data including customCSS"""
    # SECURITY: Sanitize theme_id to prevent path traversal
    if not re.match(r'^[a-zA-Z0-9_-]+$', theme_id):
        return jsonify({'error': 'Invalid theme ID'}), 400

    themes_dir = Path(__file__).parent / 'static' / 'themes'
    theme_file = themes_dir / f'{theme_id}.json'

    if not theme_file.exists():
        return jsonify({'error': 'Theme not found'}), 404

    try:
        with open(theme_file, 'r') as f:
            theme_data = json.load(f)
        return jsonify(theme_data)
    except (json.JSONDecodeError, IOError) as e:
        app.logger.error(f"Theme load error for {theme_id}: {e}")
        return jsonify({'error': 'Failed to load theme'}), 500


# ============================================
# POOL API ROUTES
# ============================================

@app.route('/api/pool/stats', methods=['GET'])
@api_key_or_login_required
def get_pool_stats():
    """Get pool statistics from Spiral Stratum API"""
    stats = fetch_pool_stats()
    return jsonify(stats)


@app.route('/api/pool/metrics', methods=['GET'])
@api_key_or_login_required
def get_pool_metrics():
    """Get Prometheus metrics from the pool"""
    metrics = fetch_prometheus_metrics()

    # Extract key metrics for display
    key_metrics = {
        "connections_active": metrics.get("stratum_connections_active", 0),
        "connections_total": metrics.get("stratum_connections_total", 0),
        "shares_submitted": metrics.get("stratum_shares_submitted_total", 0),
        "shares_accepted": metrics.get("stratum_shares_accepted_total", 0),
        "shares_stale": metrics.get("stratum_shares_stale_total", 0),
        "blocks_found": metrics.get("stratum_blocks_found_total", 0),
        "blocks_confirmed": metrics.get("stratum_blocks_confirmed_total", 0),
        "blocks_orphaned": metrics.get("stratum_blocks_orphaned_total", 0),
        "pool_hashrate": metrics.get("stratum_hashrate_pool_hps", 0),
        "network_hashrate": metrics.get("stratum_hashrate_network_hps", 0),
        "network_difficulty": metrics.get("stratum_network_difficulty", 0),
        "goroutines": metrics.get("stratum_goroutines_count", 0),
        "memory_bytes": metrics.get("stratum_memory_alloc_bytes", 0)
    }

    return jsonify({
        "key_metrics": key_metrics,
        "all_metrics": metrics,
        "last_update": prometheus_cache["last_update"]
    })


@app.route('/api/pool/history', methods=['GET'])
@api_key_or_login_required
def get_pool_history():
    """Get historical data for charts"""
    # Get time range from query params
    hours = request.args.get('hours', 24, type=int)
    hours = min(hours, 720)  # Max 30 days

    cutoff = time.time() - (hours * 3600)

    def filter_by_time(data):
        return [d for d in data if d["time"] >= cutoff]

    return jsonify({
        "pool_hashrate": filter_by_time(list(historical_data["pool_hashrate"])),
        "miner_hashrate": filter_by_time(list(historical_data["miner_hashrate"])),
        "connected_miners": filter_by_time(list(historical_data["connected_miners"])),
        "shares_per_second": filter_by_time(list(historical_data["shares_per_second"])),
        "power_watts": filter_by_time(list(historical_data["power_watts"])),
        "network_difficulty": filter_by_time(list(historical_data["network_difficulty"])),
        "network_hashrate": filter_by_time(list(historical_data["network_hashrate"])),
        "hours": hours
    })


@app.route('/api/alerts', methods=['GET'])
@api_key_or_login_required
def get_alerts():
    """Get current alerts"""
    return jsonify({
        "alerts": alert_state.get("alerts_triggered", []),
        "config": alert_config
    })


@app.route('/api/alerts/config', methods=['GET', 'POST'])
@api_key_or_login_required
def alerts_config():
    """Get or update alert configuration"""
    global alert_config

    if request.method == 'POST':
        new_config = request.json
        if not isinstance(new_config, dict):
            return jsonify({"success": False, "error": "Invalid JSON body"}), 400
        # Type-validate boolean field
        if "enabled" in new_config and isinstance(new_config["enabled"], bool):
            alert_config["enabled"] = new_config["enabled"]
        # Type-validate numeric fields with reasonable range clamping
        for key in ["hashrate_drop_percent", "miner_offline_minutes",
                    "temp_warning", "temp_critical", "check_interval"]:
            if key in new_config:
                try:
                    val = float(new_config[key])
                    val = max(0, min(val, 100000))
                    alert_config[key] = val
                except (TypeError, ValueError):
                    pass
        return jsonify({"success": True, "config": alert_config})

    return jsonify(alert_config)


@app.route('/api/combined', methods=['GET'])
@api_key_or_login_required
def get_combined_stats():
    """Get all stats in one call for dashboard efficiency.

    Multi-coin support:
    - Includes enabled coins and primary coin info
    - Includes multi-coin mode flag
    - Includes stratum ports for all enabled coins
    """
    # HA BACKUP SAFETY NET: Don't trigger miner polling on BACKUP nodes
    is_backup = ha_status_cache.get("enabled") and ha_status_cache.get("local_role") not in ("MASTER", "STANDALONE", "UNKNOWN")
    if not is_backup and time.time() - miner_cache["last_update"] > 90:
        fetch_all_miners()

    pool_stats = fetch_pool_stats()
    reward_info = fetch_block_reward()
    prometheus_metrics = fetch_prometheus_metrics()

    # Fetch HA status (only included if HA is enabled)
    ha_status = fetch_ha_status()

    # Get multi-coin info
    coins_info = get_enabled_coins()
    enabled_coins = coins_info.get("enabled", [])
    primary_coin = coins_info.get("primary")  # No default - use detected coin
    multi_coin_mode = coins_info.get("multi_coin_mode", False)

    # Calculate efficiency
    total_shares = miner_cache["totals"].get("accepted_shares", 0)
    rejected = miner_cache["totals"].get("rejected_shares", 0)
    efficiency = (total_shares / (total_shares + rejected) * 100) if (total_shares + rejected) > 0 else 100

    response = {
        "miners": miner_cache["miners"],
        "miner_totals": miner_cache["totals"],
        "pool_stats": pool_stats,
        "block_reward": reward_info,
        "lifetime": lifetime_stats,
        "alerts": alert_state.get("alerts_triggered", []),
        "efficiency": round(efficiency, 2),
        "prometheus": {
            "shares_accepted": prometheus_metrics.get("stratum_shares_accepted_total", 0),
            "blocks_found": prometheus_metrics.get("stratum_blocks_found_total", 0),
            "pool_hashrate_hps": prometheus_metrics.get("stratum_hashrate_pool_hps", 0)
        },
        "last_update": time.time(),
        # Multi-coin info
        "coins": {
            "primary": primary_coin,
            "enabled": enabled_coins,
            "multi_coin_mode": multi_coin_mode
        }
    }

    # Add per-coin info with health status
    # Always include coin details (not just multi-coin mode) for dashboard display
    all_nodes_health = fetch_all_nodes_health()
    coin_details = {}
    working_coins = []

    for symbol in enabled_coins:
        node = MULTI_COIN_NODES.get(symbol, {})
        health = all_nodes_health.get(symbol, {})
        is_online = health.get("status") == "online"

        if is_online:
            working_coins.append(symbol)

        # Include merge-mining metadata for frontend badges
        mm = node.get("merge_mining")
        coin_details[symbol] = {
            "name": node.get("name", symbol),
            "stratum_port": node.get("stratum_ports", {}).get("v1", 3333),
            "stratum_port_tls": node.get("stratum_ports", {}).get("tls", 3335),
            "block_reward": COIN_BLOCK_REWARDS.get(symbol, 0),
            "block_time": COIN_BLOCK_TIMES.get(symbol, 15),
            # Add health status for UI filtering
            "status": health.get("status", "unknown"),
            "online": is_online,
            "blocks": health.get("blocks", 0),
            "sync_progress": health.get("sync_progress", 0),
            "connections": health.get("connections", 0),
            # Merge-mining relationship metadata
            "merge_mining": {
                "role": mm["role"],
                "parent_chain": mm.get("parent_chain"),
                "aux_chains": mm.get("aux_chains"),
                "merge_only": mm.get("merge_only", False),
            } if mm else None,
        }

    response["coins"]["details"] = coin_details
    response["coins"]["working"] = working_coins
    response["coins"]["working_count"] = len(working_coins)

    # System warnings for operator awareness
    system_warnings = []

    # BC2 (Bitcoin II) specific warning about address format confusion
    if "BC2" in enabled_coins:
        bc2_health = all_nodes_health.get("BC2", {})
        if bc2_health.get("status") == "online":
            system_warnings.append({
                "id": "bc2_address_format",
                "severity": "warning",
                "coin": "BC2",
                "title": "Bitcoin II Address Format Warning",
                "message": "BC2 uses identical address formats to Bitcoin (bc1q, 1, 3). "
                          "Verify your mining address was generated by Bitcoin II Core, NOT Bitcoin Core. "
                          "Extended confirmations (100-200 blocks) recommended due to lower network hashrate.",
                "docs_link": "/docs/BITCOIN-II.md#block-confirmation-guidelines",
                "dismissible": True
            })

    # SYS merge-only warning (cannot solo mine)
    if "SYS" in enabled_coins:
        system_warnings.append({
            "id": "sys_merge_only",
            "severity": "info",
            "coin": "SYS",
            "title": "Syscoin: Merge-Mining Only",
            "message": "Syscoin requires BTC as parent chain (merge-mining only). "
                      "Solo mining SYS is not supported due to CbTx/quorum commitment requirements.",
            "dismissible": True
        })

    # Node sync warnings
    for symbol, health in all_nodes_health.items():
        if health.get("status") == "syncing":
            sync_pct = health.get("sync_progress", 0)
            system_warnings.append({
                "id": f"{symbol.lower()}_syncing",
                "severity": "info",
                "coin": symbol,
                "title": f"{symbol} Node Syncing",
                "message": f"{symbol} node is still syncing ({sync_pct:.1f}%). Mining will work but may miss some block templates.",
                "dismissible": True
            })
        elif health.get("status") == "offline":
            system_warnings.append({
                "id": f"{symbol.lower()}_offline",
                "severity": "error",
                "coin": symbol,
                "title": f"{symbol} Node Offline",
                "message": f"{symbol} node is not responding. Mining for this coin is unavailable.",
                "dismissible": False
            })

    response["system_warnings"] = system_warnings

    # Only include HA info if HA is enabled (keeps response clean for single-node setups)
    if ha_status.get("enabled", False):
        response["ha"] = {
            "enabled": True,
            "state": ha_status.get("state", "unknown"),
            "vip": ha_status.get("vip", ""),
            "stratum_address": ha_status.get("stratum_address", ""),
            "local_role": ha_status.get("local_role", "UNKNOWN"),
            "master_host": ha_status.get("master_host", ""),
            "node_count": ha_status.get("node_count", 0),
            "healthy_nodes": ha_status.get("healthy_nodes", 0),
            "failover_count": ha_status.get("failover_count", 0)
        }

    return jsonify(response)


@app.route('/api/health', methods=['GET'])
@api_key_or_login_required
def get_health():
    """Get pool and node health status"""
    health = fetch_health_data()
    return jsonify(health)


@app.route('/api/pool/local-addresses', methods=['GET'])
@api_key_or_login_required
def get_local_pool_addresses_api():
    """
    Debug endpoint to see what addresses are recognized as the local Spiral Stratum pool.

    This is useful for troubleshooting when miners show as 'external' when they should be 'local'.
    Returns all address patterns that will match as "connected to this pool".
    """
    addresses = get_local_pool_addresses()

    # Get current miner pool connections for comparison
    miner_connections = {}
    if miner_cache and miner_cache.get("miners"):
        for name, data in miner_cache["miners"].items():
            pool_url = data.get("pool_url", "")
            is_local = data.get("is_local_pool")
            miner_connections[name] = {
                "pool_url": pool_url,
                "is_local_pool": is_local,
                "status": data.get("pool_connection_status", "unknown")
            }

    return jsonify({
        "success": True,
        "local_addresses": sorted(list(addresses)),
        "address_count": len(addresses),
        "miner_connections": miner_connections,
        "totals": {
            "local_pool_count": miner_cache.get("totals", {}).get("local_pool_count", 0),
            "external_pool_count": miner_cache.get("totals", {}).get("external_pool_count", 0),
            "online_count": miner_cache.get("totals", {}).get("online_count", 0)
        },
        "note": "If a miner shows as 'external' but should be 'local', add its pool_url pattern to local_addresses"
    })


@app.route('/api/pool/local-addresses', methods=['POST'])
@admin_required
def add_local_pool_address():
    """
    Add an address pattern to the local pool address list (runtime, not persisted).

    POST body: {"address": "192.168.1.100:3333"}

    This is useful when miners are connecting to an address not auto-detected.
    The change persists until dashboard restart. For permanent changes, set
    the LOCAL_POOL_ADDRESSES environment variable.
    """
    global _local_pool_addresses

    data = request.json or {}
    address = data.get("address", "").strip().lower()

    if not address:
        return jsonify({"success": False, "error": "address is required"}), 400

    # Initialize if needed
    if _local_pool_addresses is None:
        get_local_pool_addresses()

    # Add the address
    _local_pool_addresses.add(address)

    # Also add with common protocol prefixes
    _local_pool_addresses.add(f"stratum+tcp://{address}")
    _local_pool_addresses.add(f"stratum+ssl://{address}")

    return jsonify({
        "success": True,
        "message": f"Added {address} to local pool addresses",
        "address_count": len(_local_pool_addresses),
        "note": "This change is not persisted. Set LOCAL_POOL_ADDRESSES env var for permanent changes."
    })


@app.route('/api/health/live', methods=['GET'])
def get_health_live():
    """
    Liveness probe - returns 200 if dashboard is running.
    Used by systemd watchdog and load balancers.
    Minimal check for fast response.
    """
    return jsonify({
        "status": "alive",
        "timestamp": datetime.utcnow().isoformat() + "Z"
    }), 200


@app.route('/api/health/ready', methods=['GET'])
def get_health_ready():
    """
    Readiness probe - returns 200 if dashboard can serve requests.
    Checks if critical dependencies are available.
    Used by load balancers to determine if traffic should be sent here.
    """
    ready = True
    checks = {
        "dashboard": True,
        "pool_api": False,
        "timestamp": datetime.utcnow().isoformat() + "Z"
    }

    # Check if pool API is reachable
    try:
        response = _http_session.get(f"{POOL_API_URL}/api/pools", timeout=3)
        if response.status_code == 200:
            checks["pool_api"] = True
    except Exception:
        checks["pool_api"] = False
        ready = False

    checks["ready"] = ready

    status_code = 200 if ready else 503
    return jsonify(checks), status_code


@app.route('/api/ha/status', methods=['GET'])
@api_key_or_login_required
def get_ha_status():
    """
    Get High Availability cluster status.
    Returns HA info only if HA mode is detected and enabled.
    """
    ha_status = fetch_ha_status()

    if not ha_status.get("enabled", False):
        return jsonify({
            "enabled": False,
            "message": "HA mode is not enabled. Pool is running in single-node mode."
        })

    return jsonify({
        "enabled": True,
        "state": ha_status.get("state", "unknown"),
        "vip": ha_status.get("vip", ""),
        "vip_interface": ha_status.get("vip_interface", ""),
        "stratum_address": ha_status.get("stratum_address", ""),
        "local_role": ha_status.get("local_role", "UNKNOWN"),
        "local_id": ha_status.get("local_id", ""),
        "master_id": ha_status.get("master_id", ""),
        "master_host": ha_status.get("master_host", ""),
        "node_count": ha_status.get("node_count", 0),
        "healthy_nodes": ha_status.get("healthy_nodes", 0),
        "failover_count": ha_status.get("failover_count", 0),
        "miner_connection_tip": f"Configure miners to connect to: stratum+tcp://{ha_status.get('stratum_address', '')}"
    })


# ============================================
# MULTI-COIN NODE ADMIN API ENDPOINTS
# ============================================

@app.route('/api/nodes', methods=['GET'])
@api_key_or_login_required
def get_all_nodes():
    """Get health status for all coin nodes"""
    nodes = fetch_all_nodes_health()
    return jsonify({
        "success": True,
        "nodes": nodes,
        "last_update": time.time()
    })


@app.route('/api/nodes/working', methods=['GET'])
@api_key_or_login_required
def get_working_nodes():
    """Get only enabled AND healthy/online coin nodes.

    This endpoint returns coins that are:
    1. Enabled in the configuration
    2. Online and responding to RPC calls

    Use this for UI elements that should only show working coins.

    Multi-coin mode detection:
    - If 2+ coins are working, multi_coin_mode is True
    - Returns working_coins array with symbols
    - Returns primary_coin (first working coin)

    Returns:
        JSON with working nodes, multi_coin_mode flag, and primary coin
    """
    all_nodes = fetch_all_nodes_health()

    working_nodes = {}
    working_coins = []

    for symbol, health in all_nodes.items():
        # A coin is "working" if it's both enabled AND online
        if health.get('enabled', False) and health.get('status') == 'online':
            working_nodes[symbol] = health
            working_coins.append(symbol)

    # Determine primary coin from working coins
    # Alphabetical order (no coin preference - user configures primary with 'primary: true' in config)
    primary_coin = None
    if working_coins:
        # Sort alphabetically to ensure deterministic behavior with no coin bias
        sorted_coins = sorted(working_coins)
        primary_coin = sorted_coins[0]

    # Multi-coin mode if 2+ coins are working
    multi_coin_mode = len(working_coins) > 1

    return jsonify({
        "success": True,
        "working_nodes": working_nodes,
        "working_coins": working_coins,
        "working_count": len(working_coins),
        "primary_coin": primary_coin,
        "multi_coin_mode": multi_coin_mode,
        "last_update": time.time(),
        "note": "Only returns coins that are both enabled AND online"
    })


@app.route('/api/nodes/<symbol>', methods=['GET'])
@api_key_or_login_required
def get_node_health(symbol):
    """Get health status for a specific coin node"""
    health = fetch_coin_node_health(symbol.upper())
    return jsonify({
        "success": True,
        "node": health
    })


@app.route('/api/stratum/restart', methods=['POST'])
@admin_required
def restart_stratum():
    """Restart the Spiral Stratum service (requires sudo permissions)"""
    # SECURITY: Rate limiting for service control endpoints
    client_ip = request.remote_addr or "unknown"
    if not check_rate_limit(client_ip, "stratum_restart"):
        return jsonify({"success": False, "error": "Rate limit exceeded. Please wait before trying again."}), 429

    try:
        import subprocess
        result = subprocess.run(
            ['sudo', 'systemctl', 'restart', 'spiralstratum'],
            capture_output=True,
            text=True,
            timeout=30
        )
        if result.returncode == 0:
            app.logger.info(f"Spiral Stratum restarted by {client_ip}")
            return jsonify({
                "success": True,
                "message": "Spiral Stratum restart initiated"
            })
        else:
            app.logger.warning(f"Spiral Stratum restart failed for {client_ip}")
            return jsonify({
                "success": False,
                "error": "Service restart failed. Check system logs for details."
            })
    except Exception as e:
        app.logger.error(f"Stratum restart exception: {e}")
        return jsonify({"success": False, "error": "Failed to restart Spiral Stratum"})


@app.route('/api/nodes/<symbol>/restart', methods=['POST'])
@admin_required
def restart_node(symbol):
    """Restart a specific coin node (requires sudo permissions)"""
    # SECURITY: Rate limiting for service control endpoints
    client_ip = request.remote_addr or "unknown"
    if not check_rate_limit(client_ip, "node_restart"):
        return jsonify({"success": False, "error": "Rate limit exceeded. Please wait before trying again."}), 429

    # SECURITY: Validate symbol against whitelist
    symbol = symbol.upper()
    if symbol not in MULTI_COIN_NODES:
        return jsonify({"success": False, "error": f"Unknown coin: {symbol}"}), 400

    node = MULTI_COIN_NODES[symbol]
    service_name = node['service_name']

    # SECURITY: Validate service name format (alphanumeric and hyphens only)
    if not re.match(r'^[a-zA-Z0-9-]+$', service_name):
        app.logger.error(f"Invalid service name in config: {service_name}")
        return jsonify({"success": False, "error": "Invalid service configuration"}), 500

    try:
        import subprocess
        result = subprocess.run(
            ['sudo', 'systemctl', 'restart', service_name],
            capture_output=True,
            text=True,
            timeout=30
        )
        if result.returncode == 0:
            app.logger.info(f"Node {symbol} restarted by {client_ip}")
            return jsonify({
                "success": True,
                "message": f"{node['name']} node restart initiated"
            })
        else:
            app.logger.warning(f"Node {symbol} restart failed for {client_ip}")
            return jsonify({
                "success": False,
                "error": "Service restart failed. Check system logs for details."
            })
    except Exception as e:
        app.logger.error(f"Node restart exception: {e}")
        return jsonify({"success": False, "error": "Failed to restart node"})


@app.route('/api/nodes/<symbol>/stop', methods=['POST'])
@admin_required
def stop_node(symbol):
    """Stop a specific coin node (requires sudo permissions)"""
    # SECURITY: Rate limiting for service control endpoints
    client_ip = request.remote_addr or "unknown"
    if not check_rate_limit(client_ip, "node_stop"):
        return jsonify({"success": False, "error": "Rate limit exceeded. Please wait before trying again."}), 429

    # SECURITY: Validate symbol against whitelist
    symbol = symbol.upper()
    if symbol not in MULTI_COIN_NODES:
        return jsonify({"success": False, "error": f"Unknown coin: {symbol}"}), 400

    node = MULTI_COIN_NODES[symbol]
    service_name = node['service_name']

    # SECURITY: Validate service name format (alphanumeric and hyphens only)
    if not re.match(r'^[a-zA-Z0-9-]+$', service_name):
        app.logger.error(f"Invalid service name in config: {service_name}")
        return jsonify({"success": False, "error": "Invalid service configuration"}), 500

    try:
        import subprocess
        result = subprocess.run(
            ['sudo', 'systemctl', 'stop', service_name],
            capture_output=True,
            text=True,
            timeout=30
        )
        if result.returncode == 0:
            app.logger.info(f"Node {symbol} stopped by {client_ip}")
            return jsonify({
                "success": True,
                "message": f"{node['name']} node stopped"
            })
        else:
            app.logger.warning(f"Node {symbol} stop failed for {client_ip}")
            return jsonify({
                "success": False,
                "error": "Service stop failed. Check system logs for details."
            })
    except Exception as e:
        app.logger.error(f"Node stop exception: {e}")
        return jsonify({"success": False, "error": "Failed to stop node"})


@app.route('/api/nodes/<symbol>/start', methods=['POST'])
@admin_required
def start_node(symbol):
    """Start a specific coin node (requires sudo permissions)"""
    # SECURITY: Rate limiting for service control endpoints
    client_ip = request.remote_addr or "unknown"
    if not check_rate_limit(client_ip, "node_start"):
        return jsonify({"success": False, "error": "Rate limit exceeded. Please wait before trying again."}), 429

    # SECURITY: Validate symbol against whitelist
    symbol = symbol.upper()
    if symbol not in MULTI_COIN_NODES:
        return jsonify({"success": False, "error": f"Unknown coin: {symbol}"}), 400

    node = MULTI_COIN_NODES[symbol]
    service_name = node['service_name']

    # SECURITY: Validate service name format (alphanumeric and hyphens only)
    if not re.match(r'^[a-zA-Z0-9-]+$', service_name):
        app.logger.error(f"Invalid service name in config: {service_name}")
        return jsonify({"success": False, "error": "Invalid service configuration"}), 500

    try:
        import subprocess
        result = subprocess.run(
            ['sudo', 'systemctl', 'start', service_name],
            capture_output=True,
            text=True,
            timeout=30
        )
        if result.returncode == 0:
            app.logger.info(f"Node {symbol} started by {client_ip}")
            return jsonify({
                "success": True,
                "message": f"{node['name']} node started"
            })
        else:
            app.logger.warning(f"Node {symbol} start failed for {client_ip}")
            return jsonify({
                "success": False,
                "error": "Service start failed. Check system logs for details."
            })
    except Exception as e:
        app.logger.error(f"Node start exception: {e}")
        return jsonify({"success": False, "error": "Failed to start node"})


@app.route('/api/nodes/<symbol>/sync', methods=['GET'])
@api_key_or_login_required
def get_node_sync_status(symbol):
    """Get detailed sync status for a specific coin node"""
    symbol = symbol.upper()
    if symbol not in MULTI_COIN_NODES:
        return jsonify({"success": False, "error": f"Unknown coin: {symbol}"})

    bc_info = coin_rpc(symbol, "getblockchaininfo")
    if not bc_info:
        return jsonify({
            "success": False,
            "error": f"Could not connect to {symbol} node"
        })

    blocks = bc_info.get("blocks", 0)
    headers = bc_info.get("headers", 0)
    progress = bc_info.get("verificationprogress", 0)

    # Estimate time remaining based on current sync rate
    sync_status = {
        "symbol": symbol,
        "blocks": blocks,
        "headers": headers,
        "progress_percent": round(progress * 100, 4),
        "blocks_remaining": headers - blocks if headers > blocks else 0,
        "is_synced": progress >= 0.9999,
        "chain": bc_info.get("chain", ""),
        "size_on_disk_gb": round(bc_info.get("size_on_disk", 0) / 1024 / 1024 / 1024, 2),
        "pruned": bc_info.get("pruned", False)
    }

    return jsonify({
        "success": True,
        "sync": sync_status
    })


@app.route('/api/nodes/ports', methods=['GET'])
@api_key_or_login_required
def get_stratum_ports():
    """Get stratum port allocation for all coins"""
    ports = {}
    for symbol, node in MULTI_COIN_NODES.items():
        ports[symbol] = {
            "name": node['name'],
            "enabled": node['enabled'],
            "stratum_v1": node['stratum_ports']['v1'],
            "stratum_v2": node['stratum_ports']['v2'],
            "stratum_tls": node['stratum_ports']['tls'],
            "rpc_port": node['rpc_port']
        }
    return jsonify({
        "success": True,
        "ports": ports
    })


@app.route('/api/pool/stratum-address', methods=['GET'])
@api_key_or_login_required
def get_stratum_address():
    """
    Get the recommended stratum connection address for miners.

    Returns the best address based on:
    1. HA VIP address if cluster is enabled and this node is primary/backup
    2. Server's public IP/hostname with stratum port

    In multi-coin mode, returns connection info for ALL enabled coins.

    Security: Validates all returned data, no user input processed.
    OWASP: A01 (auth decorator), A03 (input validation), A09 (logging)
    """
    import socket

    # Under gunicorn, __main__ is never executed, so load_multi_coin_config() is not
    # called at startup. Lazy-init here so MULTI_COIN_NODES has RPC credentials loaded.
    if not _multi_coin_config_loaded:
        load_multi_coin_config()

    # Security: Log access for audit trail
    client_ip = request.remote_addr or "unknown"
    app.logger.info(f"Stratum address requested from {client_ip}")

    # Hostname validation regex - requires 2+ chars, valid DNS format
    # Allows: alphanumeric, hyphens (not at start/end), dots (for subdomains)
    HOSTNAME_REGEX = re.compile(r'^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$')

    def is_valid_hostname(hostname: str) -> bool:
        """Validate hostname format per RFC 1123."""
        if not hostname or len(hostname) > 253:
            return False
        return bool(HOSTNAME_REGEX.match(hostname))

    def is_valid_ip(ip: str) -> bool:
        """Validate IPv4 address format."""
        try:
            socket.inet_aton(ip)
            return True
        except socket.error:
            return False

    # Check HA status first
    ha_status = fetch_ha_status()
    ha_enabled = ha_status.get("enabled", False)
    ha_role = ha_status.get("local_role", "UNKNOWN") if ha_enabled else None
    vip = ha_status.get("vip", "") if ha_enabled else None

    # Validate VIP if present
    if vip and not (is_valid_ip(vip) or is_valid_hostname(vip)):
        vip = None  # Invalid VIP, ignore it

    # Determine the host address to use
    host = None
    address_source = None

    # Priority 1: HA VIP
    if vip:
        host = vip
        address_source = "HA VIP address" if is_valid_ip(vip) else "HA VIP hostname"

    # Priority 2: Configured external address
    if not host:
        try:
            _install = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
            config_paths = [
                Path(_install) / "config" / "config.yaml",
                Path("/spiralpool/config/config.yaml"),
                Path("/etc/spiralpool/config.yaml"),
            ]

            for config_path in config_paths:
                if config_path.exists():
                    with open(config_path, 'r') as f:
                        import yaml
                        pool_config = yaml.safe_load(f)

                    if pool_config and 'stratum' in pool_config:
                        stratum_cfg = pool_config['stratum']
                        if stratum_cfg.get('externalAddress'):
                            addr = str(stratum_cfg['externalAddress'])
                            # Extract host part (handle host:port format)
                            addr_host = addr.split(':')[0] if ':' in addr else addr
                            if is_valid_ip(addr_host) or is_valid_hostname(addr_host):
                                host = addr_host
                                address_source = "configured external address"
                    break
        except Exception as e:
            app.logger.debug(f"Could not read pool config for stratum address: {e}")

    # Priority 3: Auto-detect server IP
    if not host:
        try:
            # Get the IP that would be used to connect to the internet
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.settimeout(2)
            s.connect(("8.8.8.8", 80))
            host = s.getsockname()[0]
            s.close()
            address_source = "server's network IP"
        except Exception:
            pass

    # Priority 4: Hostname fallback
    if not host:
        try:
            host = socket.gethostname()
            address_source = "server hostname"
        except Exception:
            pass

    # Load wallet addresses from pool config
    # SECURITY: Uses global VALID_COINS and validate_wallet_address() for proper validation
    # Extended whitelist includes long-form names that get normalized to standard symbols
    VALID_COIN_TYPES_EXTENDED = {"DGB", "BTC", "BCH", "BC2", "LTC", "DOGE", "DGB-SCRYPT",
                                  "PEP", "CAT", "NMC", "SYS", "XMY", "FBTC", "QBX",
                                  "DIGIBYTE", "BITCOIN", "BITCOINCASH", "BITCOINII", "BITCOIN2",
                                  "LITECOIN", "DOGECOIN", "DIGIBYTE-SCRYPT",
                                  "PEPECOIN", "CATCOIN",
                                  "NAMECOIN", "SYSCOIN", "MYRIADCOIN", "FRACTALBITCOIN", "FRACTAL",
                                  "QBITX", "Q-BITX"}

    wallet_addresses = {}
    try:
        import yaml
        if os.path.exists(POOL_CONFIG_PATH):
            with open(POOL_CONFIG_PATH, 'r') as f:
                config = yaml.safe_load(f)
                # config.yaml uses 'coins' key (written by save_pool_coin_config).
                # Some stratum-native configs use 'pools' key — check both.
                raw_entries = config.get('coins', []) or config.get('pools', [])
                for pool in raw_entries:
                    if not isinstance(pool, dict):
                        continue
                    # Dashboard 'coins' format: {symbol, address, ...}
                    # Stratum 'pools' format: {coin: {type: "DGB"}, address: "..."}
                    coin_info_cfg = pool.get('coin', {})
                    if coin_info_cfg:
                        # Stratum-native format: coin.type
                        if isinstance(coin_info_cfg, dict):
                            coin_type = coin_info_cfg.get('type', '').upper()
                        elif isinstance(coin_info_cfg, str):
                            coin_type = coin_info_cfg.upper()
                        else:
                            continue
                    else:
                        # Dashboard format: symbol key at top level
                        coin_type = pool.get('symbol', '').upper()
                    if not coin_type:
                        continue

                    # SECURITY: Validate coin type against whitelist (A03)
                    if coin_type not in VALID_COIN_TYPES_EXTENDED:
                        app.logger.warning(f"Skipping invalid coin type in config: {coin_type[:20]}")
                        continue

                    # Normalize coin types to standard symbols (DGB, BTC, BCH, BC2, LTC, DOGE)
                    # Use dict for exact key matching (order doesn't matter)
                    coin_normalize_map = {
                        "DIGIBYTE": "DGB",
                        "BITCOINCASH": "BCH", "BITCOIN-CASH": "BCH",
                        "BITCOINII": "BC2", "BITCOIN-II": "BC2", "BITCOIN2": "BC2", "BCII": "BC2",
                        "BITCOIN": "BTC",
                        "LITECOIN": "LTC",
                        "DOGECOIN": "DOGE",
                        "DIGIBYTE-SCRYPT": "DGB-SCRYPT",
                        "NAMECOIN": "NMC",
                        "SYSCOIN": "SYS",
                        "MYRIADCOIN": "XMY", "MYRIAD": "XMY",
                        "FRACTALBITCOIN": "FBTC", "FRACTAL-BITCOIN": "FBTC",
                        "QBITX": "QBX", "Q-BITX": "QBX",
                        "PEPECOIN": "PEP",
                        "CATCOIN": "CAT",
                    }
                    coin_type = coin_normalize_map.get(coin_type, coin_type)

                    wallet_addr = pool.get('address', '')
                    # SECURITY: Validate wallet address format using proper crypto validation (A03, A08)
                    # Uses validate_wallet_address() which has coin-specific regex patterns
                    if wallet_addr and isinstance(wallet_addr, str):
                        wallet_addr = wallet_addr.strip()
                        # Use the proper validate_wallet_address function with coin-specific patterns
                        if validate_wallet_address(coin_type, wallet_addr):
                            wallet_addresses[coin_type] = wallet_addr
                        else:
                            app.logger.warning(f"Skipping invalid wallet address format for {coin_type}")
    except Exception as e:
        app.logger.debug(f"Could not load wallet addresses from config: {e}")

    # Build response with all enabled coins
    coins = []
    active_coin = ACTIVE_COIN_SYMBOL  # No default - use detected coin or None

    for symbol, node in MULTI_COIN_NODES.items():
        if not node.get('enabled', False):
            continue

        port_v1 = node.get('stratum_ports', {}).get('v1', 3333)
        port_tls = node.get('stratum_ports', {}).get('tls', 3335)

        # Include merge-mining metadata so frontend can display parent/aux badges
        mm = node.get('merge_mining')
        coin_info = {
            "symbol": symbol,
            "name": node.get('name', symbol),
            "is_active": symbol == active_coin,
            "stratum_port": port_v1,
            "stratum_port_tls": port_tls,
            "wallet_address": wallet_addresses.get(symbol, ""),
            "merge_mining": {
                "role": mm["role"],
                "parent_chain": mm.get("parent_chain"),
                "aux_chains": mm.get("aux_chains"),
                "merge_only": mm.get("merge_only", False),
            } if mm else None,
        }

        if host:
            coin_info["stratum_address"] = f"{host}:{port_v1}"
            coin_info["stratum_address_tls"] = f"{host}:{port_tls}"
            coin_info["connection_string"] = f"stratum+tcp://{host}:{port_v1}"
            coin_info["connection_string_tls"] = f"stratum+ssl://{host}:{port_tls}"

        coins.append(coin_info)

    # Sort: active coin first, then alphabetically
    coins.sort(key=lambda c: (0 if c["is_active"] else 1, c["symbol"]))

    # Get primary coin info for backwards compatibility
    # Prefer active coin, else alphabetically first coin
    primary_coin = next((c for c in coins if c["is_active"]),
                        (sorted(coins, key=lambda c: c["symbol"])[0] if coins else None))

    result = {
        "success": True,
        "ha_enabled": ha_enabled,
        "ha_role": ha_role,
        "host": host,
        "note": f"Using {address_source}" if address_source else "Could not determine server address",
        # Multi-coin support
        "multi_coin": len(coins) > 1,
        "coins": coins,
        # Backwards compatibility: primary coin info at top level
        "coin": primary_coin["symbol"] if primary_coin else None,
        "coin_name": primary_coin["name"] if primary_coin else None,
        "stratum_port": primary_coin["stratum_port"] if primary_coin else 3333,
        "stratum_port_tls": primary_coin["stratum_port_tls"] if primary_coin else 3335,
        "stratum_address": primary_coin.get("stratum_address") if primary_coin else None,
        "stratum_address_tls": primary_coin.get("stratum_address_tls") if primary_coin else None,
        "connection_string": primary_coin.get("connection_string") if primary_coin else None,
        "connection_string_tls": primary_coin.get("connection_string_tls") if primary_coin else None,
        "wallet_address": primary_coin.get("wallet_address") if primary_coin else None
    }

    if not host:
        result["error"] = "Could not determine server address"
        result["note"] = "Configure stratum.externalAddress in config.yaml"

    # Add warnings for coins that need special attention
    warnings = []
    enabled_symbols = [c["symbol"] for c in coins]

    # BC2 address format confusion warning (identical to BTC addresses)
    if "BC2" in enabled_symbols:
        warnings.append({
            "coin": "BC2",
            "severity": "warning",
            "message": "BC2 uses identical address formats to Bitcoin (bc1q, 1, 3). "
                      "Verify your mining address was generated by Bitcoin II Core, NOT Bitcoin Core."
        })

    # SYS merge-only warning (cannot solo mine due to CbTx/quorum commitment)
    if "SYS" in enabled_symbols:
        warnings.append({
            "coin": "SYS",
            "severity": "info",
            "message": "Syscoin is merge-mining only (requires BTC as parent chain). "
                      "Solo mining SYS is not supported due to CbTx/quorum commitment requirements."
        })

    if warnings:
        result["warnings"] = warnings

    return jsonify(result)


@app.route('/api/stratum/connect', methods=['GET'])
@api_key_or_login_required
def get_stratum_connect_info():
    """
    Get stratum connection info for pointing miners to this pool.

    This is a simplified alias for /api/pool/stratum-address that returns
    the essential information needed to configure a miner's pool settings.

    Returns:
        - host: The pool server address
        - port: The stratum port (v1)
        - wallet_address: The configured wallet address for mining rewards
        - connection_string: Full stratum connection URL (stratum+tcp://host:port)

    Used by the dashboard Pool Configuration modal to auto-fill miner settings.

    Security: A01 (auth), A09 (logging)
    """
    # SECURITY A09: Audit log access to stratum connection info
    client_ip = request.remote_addr or "unknown"
    app.logger.info(f"Stratum connect info requested from {client_ip}")

    # Call the main stratum address function and extract key fields
    with app.test_request_context():
        response = get_stratum_address()
        data = response.get_json()

    if not data.get("success"):
        return jsonify({
            "success": False,
            "error": data.get("error", "Could not get stratum info")
        })

    return jsonify({
        "success": True,
        "host": data.get("host"),
        "port": data.get("stratum_port", 3333),
        "wallet_address": data.get("wallet_address", ""),
        "connection_string": data.get("connection_string"),
        "coin": data.get("coin"),
        "coin_name": data.get("coin_name")
    })


@app.route('/api/devices/add-discovered', methods=['POST'])
@admin_required
def add_discovered_devices():
    """Add discovered devices to configuration"""
    data = request.json
    devices_to_add = data.get("devices", [])

    if not devices_to_add:
        return jsonify({"success": False, "error": "No devices provided"})

    config = load_config()

    # Default power consumption by device type (in watts)
    default_watts = {
        "axeos": 80,           # BitAxe Gamma/Ultra/Supra (~500 GH/s)
        "nmaxe": 20,           # NMaxe (~500 GH/s, 20W)
        "nerdqaxe": 80,        # NerdQAxe++ (~5 TH/s)
        "esp32miner": 2,        # ESP32 Miner: ESP32 only, ~1-2W
        "qaxe": 80,            # QAxe quad-ASIC (~2 TH/s)
        "qaxeplus": 100,       # QAxe+ enhanced cooling
        "avalon": 140,         # Avalon Nano 3 (~6.5 TH/s)
        "antminer": 3250,      # S19 default
        "antminer_scrypt": 3250, # L-series (L3+, L7, L9)
        "whatsminer": 3400,    # M30S default
        "innosilicon": 3500,   # A10 Pro default
        "goldshell": 2300,     # KD6/LT5 default (~2000-3000W range)
        "hammer": 25,          # PlebSource Hammer Miner (Scrypt, 105 MH/s)
        "futurebit": 200,      # FutureBit Apollo (125-375W range)
        "braiins": 3250,       # BraiinsOS (S9/S17/S19/S21): varies by model
        "vnish": 3250,         # Vnish firmware (Antminer variants)
        "luxos": 3250,         # LuxOS firmware (Antminer variants)
        "luckyminer": 50,      # Lucky Miner LV06/LV07/LV08 (25-200W range)
        "jingleminer": 100,    # Jingle Miner BTC Solo Pro/Lite (50-200W)
        "zyber": 100,          # Zyber 8G/8GP/8S (TinyChipHub, ~100W)
        "gekkoscience": 5,     # GekkoScience USB miners (5-15W)
        "ipollo": 2000,        # iPollo V1/G1 series (~2000W)
        "ebang": 2800,         # Ebang/Ebit E9-E12 (~2800W)
        "epic": 3000,          # ePIC BlockMiner (~3000W)
        "elphapex": 3000,      # Elphapex DG1/DG Home (Scrypt, ~3000W)
        "canaan": 3000         # Canaan AvalonMiner A13/A14 series (~3000W)
    }

    # Valid device types whitelist
    valid_types = ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "avalon", "antminer", "whatsminer", "innosilicon", "goldshell", "hammer", "futurebit", "antminer_scrypt", "braiins", "vnish", "luxos", "luckyminer", "jingleminer", "zyber", "gekkoscience", "ipollo", "ebang", "epic", "elphapex", "canaan"]

    for device in devices_to_add:
        device_type = device.get("type")
        if device_type not in valid_types:
            continue

        # SECURITY: Validate IP address to prevent SSRF attacks
        ip_str = device.get("ip", "")
        try:
            ip_obj = ipaddress.ip_address(ip_str)
            # Only allow private network IPs for device discovery (block loopback to prevent SSRF)
            if not ip_obj.is_private or ip_obj.is_loopback:
                continue  # Skip public and loopback IPs
        except ValueError:
            continue  # Skip invalid IPs

        # SECURITY: Sanitize device name (alphanumeric, dash, underscore, dot only)
        raw_name = device.get("name", ip_str)
        sanitized_name = re.sub(r'[^a-zA-Z0-9._-]', '', str(raw_name)[:64])
        if not sanitized_name:
            sanitized_name = ip_str

        # SECURITY: Validate watts value (reasonable range for mining devices)
        watts_val = device.get("watts", default_watts.get(device_type, 100))
        try:
            watts_val = int(watts_val)
            watts_val = max(1, min(watts_val, 10000))  # Clamp to 1-10000W
        except (TypeError, ValueError):
            watts_val = default_watts.get(device_type, 100)

        new_device = {
            "name": sanitized_name,
            "ip": ip_str,
            "watts": watts_val
        }

        # Add port for CGMiner-based devices (goldshell uses HTTP port 80, others use 4028)
        if device_type in ["avalon", "antminer", "antminer_scrypt", "whatsminer", "innosilicon", "futurebit", "goldshell"]:
            default_port = 80 if device_type == "goldshell" else 4028
            port_val = device.get("port", default_port)
            try:
                port_val = int(port_val)
                port_val = max(1, min(port_val, 65535))  # Valid port range
            except (TypeError, ValueError):
                port_val = default_port
            new_device["port"] = port_val

        # Check if already exists
        existing_ips = [d["ip"] for d in config["devices"].get(device_type, [])]
        if ip_str not in existing_ips:
            if device_type not in config["devices"]:
                config["devices"][device_type] = []
            config["devices"][device_type].append(new_device)

    # Mark first run complete
    config["first_run"] = False
    save_config(config)

    # Sync miners to Sentinel's database so it can monitor them
    try:
        synced, sync_errors = sync_miners_to_sentinel()
        if sync_errors:
            print(f"[SYNC] Warnings during sync: {sync_errors}")
    except Exception as e:
        print(f"[SYNC] Error syncing to Sentinel: {e}")

    return jsonify({
        "success": True,
        "config": config
    })


# ============================================
#  BLOCK EXPLORER INTEGRATION
# ============================================

def get_block_explorer(coin=None):
    """Get the block explorer URLs for a coin (defaults to primary coin)"""
    if coin is None:
        coin = get_primary_coin()
    if coin is None or coin not in BLOCK_EXPLORERS:
        # Return empty structure if coin not recognized
        return {"api": "", "web": "", "tx": "", "block": "", "address": ""}
    return BLOCK_EXPLORERS.get(coin)

def fetch_block_details(block_hash, coin=None):
    """Fetch block details from the coin's block explorer API"""
    if coin is None:
        coin = get_primary_coin()
    explorer = get_block_explorer(coin)
    api_url = explorer.get("api", "")
    if not api_url:
        return None
    try:
        response = requests.get(f"{api_url}/block/{block_hash}", timeout=10)
        if response.status_code == 200:
            return response.json()
    except Exception as e:
        print(f"Error fetching block {block_hash} for {coin}: {e}")
    return None

def fetch_transaction_details(txid, coin=None):
    """Fetch transaction details from the coin's block explorer API"""
    if coin is None:
        coin = get_primary_coin()
    explorer = get_block_explorer(coin)
    api_url = explorer.get("api", "")
    if not api_url:
        return None
    try:
        response = requests.get(f"{api_url}/tx/{txid}", timeout=10)
        if response.status_code == 200:
            return response.json()
    except Exception as e:
        print(f"Error fetching tx {txid} for {coin}: {e}")
    return None

def fetch_address_transactions(address, limit=20, coin=None):
    """Fetch recent transactions for an address from the coin's block explorer"""
    if coin is None:
        coin = get_primary_coin()
    explorer = get_block_explorer(coin)
    api_url = explorer.get("api", "")
    if not api_url:
        return []
    try:
        response = requests.get(
            f"{api_url}/addr/{address}/txs?from=0&to={limit}",
            timeout=10
        )
        if response.status_code == 200:
            return response.json()
    except Exception as e:
        print(f"Error fetching address txs for {address} ({coin}): {e}")
    return []

def get_found_blocks_from_pool():
    """Get list of blocks found by the pool from the API"""
    try:
        response = requests.get(f"{POOL_API_URL}/api/pools/{get_pool_id()}/blocks", timeout=10)
        if response.status_code == 200:
            return response.json()
    except Exception as e:
        print(f"Error fetching pool blocks: {e}")
    return []


@app.route('/api/blocks/found', methods=['GET'])
@api_key_or_login_required
def get_found_blocks():
    """ Get blocks found by this pool with explorer links.

    Multi-coin support:
    - If ?coin=BTC parameter provided, returns blocks for that coin only
    - If ?all=true parameter provided, returns blocks for ALL enabled coins
    - Otherwise returns blocks for primary coin (backwards compatible)

    OWASP: A03 - Input validated against whitelist
    """
    global block_explorer_cache

    # Get enabled coins for validation
    coins_info = get_enabled_coins()
    enabled_coins = coins_info.get("enabled", [])
    primary_coin = coins_info.get("primary")  # No default - use detected coin

    # Parse query parameters
    coin_param = request.args.get('coin', '').upper()
    all_coins = request.args.get('all', 'false').lower() == 'true'

    # SECURITY: Validate coin parameter against whitelist
    if coin_param and coin_param not in MULTI_COIN_NODES:
        return jsonify({"success": False, "error": f"Invalid coin: {coin_param}"}), 400

    # Determine which coins to fetch
    if all_coins:
        # Multi-coin mode: return blocks for all enabled coins
        target_coins = enabled_coins if enabled_coins else [primary_coin]
    elif coin_param:
        # Specific coin requested
        if coin_param not in enabled_coins:
            return jsonify({"success": False, "error": f"Coin {coin_param} is not enabled"}), 400
        target_coins = [coin_param]
    else:
        # Default: primary coin only (backwards compatible)
        target_coins = [primary_coin]

    # P0 AUDIT FIX: Reduced cache TTL from 60s to 10s to catch orphans faster
    # This ensures status changes (especially orphans) are visible within 10 seconds
    if time.time() - block_explorer_cache["last_update"] > 10:
        pool_blocks = get_found_blocks_from_pool()
        enriched_blocks = []

        for block in pool_blocks[:50]:  # Last 50 blocks (more for multi-coin)
            # Determine coin for this block (from pool API or default to primary)
            block_coin = block.get("coin", primary_coin).upper()

            # Get explorer for this coin
            explorer = get_block_explorer(block_coin)
            explorer_url = explorer.get("url", "")

            block_info = {
                "height": block.get("height", 0),
                "hash": block.get("hash", ""),
                "reward": block.get("reward", 0),
                "miner": block.get("miner", ""),
                "worker": block.get("worker", ""),
                "time": block.get("created", ""),
                "confirmations": block.get("confirmations", 0),
                "explorer_url": f"{explorer_url}/block/{block.get('hash', '')}" if explorer_url else "",
                # P0 AUDIT FIX: Don't default to "confirmed" - use "unknown" if status missing
                # This prevents false confidence in block confirmation status
                "status": block.get("status", "unknown"),
                "coin": block_coin  # Include coin in response
            }
            enriched_blocks.append(block_info)

        block_explorer_cache["found_blocks"] = enriched_blocks
        block_explorer_cache["last_update"] = time.time()

    # Filter blocks by target coins
    filtered_blocks = [
        b for b in block_explorer_cache["found_blocks"]
        if b.get("coin", primary_coin) in target_coins
    ][:20]  # Limit to 20 after filtering

    # Build response
    result = {
        "success": True,
        "blocks": filtered_blocks,
        "total_found": len(filtered_blocks),
        "multi_coin": len(target_coins) > 1
    }

    if len(target_coins) == 1:
        # Single coin mode: include explorer URL and coin at top level
        coin_symbol = sorted(target_coins)[0]  # Alphabetically first (deterministic)
        explorer = get_block_explorer(coin_symbol)
        result["explorer_base_url"] = explorer.get("url", "")
        result["coin"] = coin_symbol
    else:
        # Multi-coin mode: list all target coins
        result["coins"] = target_coins
        result["explorers"] = {
            coin: get_block_explorer(coin).get("url", "")
            for coin in target_coins
        }

    return jsonify(result)


@app.route('/api/blocks/<block_hash>', methods=['GET'])
@api_key_or_login_required
def get_block_details(block_hash):
    """ Get detailed block information from explorer.

    Multi-coin support:
    - Optional ?coin=BTC parameter to specify which explorer to use
    - Defaults to primary coin

    OWASP: A03 - Block hash validated, coin validated against whitelist
    """
    # SECURITY: Validate block hash format (64 hex characters)
    if not block_hash or not re.match(r'^[a-fA-F0-9]{64}$', block_hash):
        return jsonify({"success": False, "error": "Invalid block hash format"}), 400

    # Get coin parameter (optional)
    coin_param = request.args.get('coin', '').upper()

    # SECURITY: Validate coin parameter against whitelist
    if coin_param:
        if coin_param not in MULTI_COIN_NODES:
            return jsonify({"success": False, "error": f"Invalid coin: {coin_param}"}), 400
        coin = coin_param
    else:
        coin = get_primary_coin()

    explorer = get_block_explorer(coin)
    details = fetch_block_details(block_hash, coin=coin)
    if details:
        return jsonify({
            "success": True,
            "block": details,
            "explorer_url": f"{explorer.get('url', '')}/block/{block_hash}",
            "coin": coin
        })
    return jsonify({"success": False, "error": "Block not found"}), 404


@app.route('/api/wallet/transactions', methods=['GET'])
@api_key_or_login_required
def get_wallet_transactions():
    """ Get recent transactions for pool wallet.

    Multi-coin support:
    - Optional ?coin=BTC parameter to get transactions for a specific coin
    - Optional ?all=true to get transactions for ALL enabled coins
    - Defaults to primary coin

    OWASP: A03 - Coin validated against whitelist
    """
    global block_explorer_cache

    # Get enabled coins for validation
    coins_info = get_enabled_coins()
    enabled_coins = coins_info.get("enabled", [])
    primary_coin = coins_info.get("primary")  # No default - use detected coin

    # Parse query parameters
    coin_param = request.args.get('coin', '').upper()
    all_coins = request.args.get('all', 'false').lower() == 'true'

    # SECURITY: Validate coin parameter against whitelist
    if coin_param and coin_param not in MULTI_COIN_NODES:
        return jsonify({"success": False, "error": f"Invalid coin: {coin_param}"}), 400

    # Determine which coins to fetch
    if all_coins:
        target_coins = enabled_coins if enabled_coins else [primary_coin]
    elif coin_param:
        if coin_param not in enabled_coins:
            return jsonify({"success": False, "error": f"Coin {coin_param} is not enabled"}), 400
        target_coins = [coin_param]
    else:
        target_coins = [primary_coin]

    # Collect transactions for all target coins
    all_transactions = []

    for coin in target_coins:
        explorer = get_block_explorer(coin)
        explorer_url = explorer.get("url", "")

        # Get pool wallet address for this coin
        # Try environment variable first (POOL_ADDRESS_BTC, POOL_ADDRESS_DGB, etc.)
        pool_address = os.environ.get(f"POOL_ADDRESS_{coin}", "")

        if not pool_address:
            # Fallback to generic POOL_ADDRESS for primary coin
            if coin == primary_coin:
                pool_address = os.environ.get("POOL_ADDRESS", "")

        if not pool_address:
            # Try to get from pool API
            try:
                resp = _http_session.get(f"{POOL_API_URL}/api/pools/{get_pool_id()}", timeout=5)
                if resp.status_code == 200:
                    pool_data = resp.json()
                    pool_address = pool_data.get("address", "")
            except (requests.exceptions.RequestException, ValueError, KeyError):
                pass

        if not pool_address:
            # Skip this coin if no address configured
            continue

        txs = fetch_address_transactions(pool_address, coin=coin)

        for tx in txs[:20]:
            tx_info = {
                "txid": tx.get("txid", ""),
                "time": tx.get("time", 0),
                "confirmations": tx.get("confirmations", 0),
                "value_in": sum(vin.get("value", 0) for vin in tx.get("vin", [])),
                "value_out": sum(vout.get("value", 0) for vout in tx.get("vout", [])),
                "explorer_url": f"{explorer_url}/tx/{tx.get('txid', '')}" if explorer_url else "",
                "coin": coin,
                "address": pool_address
            }
            all_transactions.append(tx_info)

    # Sort by time (newest first) and limit
    all_transactions.sort(key=lambda x: x.get("time", 0), reverse=True)
    all_transactions = all_transactions[:20]

    # Build response
    result = {
        "success": True,
        "transactions": all_transactions,
        "multi_coin": len(target_coins) > 1
    }

    if len(target_coins) == 1:
        # Single coin mode: backwards compatible response
        coin = sorted(target_coins)[0]  # Alphabetically first (deterministic)
        explorer = get_block_explorer(coin)
        pool_address = os.environ.get(f"POOL_ADDRESS_{coin}", os.environ.get("POOL_ADDRESS", ""))
        result["address"] = pool_address
        result["explorer_url"] = f"{explorer.get('url', '')}/address/{pool_address}" if pool_address else ""
        result["coin"] = coin
    else:
        # Multi-coin mode: list wallets per coin
        result["coins"] = target_coins
        result["wallets"] = {}
        for coin in target_coins:
            addr = os.environ.get(f"POOL_ADDRESS_{coin}", "")
            if not addr and coin == primary_coin:
                addr = os.environ.get("POOL_ADDRESS", "")
            if addr:
                explorer = get_block_explorer(coin)
                result["wallets"][coin] = {
                    "address": addr,
                    "explorer_url": f"{explorer.get('url', '')}/address/{addr}"
                }

    return jsonify(result)


# ============================================
#  ENHANCED HASHRATE ALERTS
# ============================================

# ============================================
# NOTE: NOTIFICATIONS (Discord/Telegram) ARE HANDLED BY SPIRAL SENTINEL
# Dashboard only tracks alerts for UI display - Sentinel sends notifications
# See: SpiralSentinel.py for notification logic
# ============================================

def log_alert_to_history(alert):
    """Log alert to history for UI display (no notifications - Sentinel handles those)"""
    alert_state["alert_history"].append(alert)
    alert_state["alert_history"] = alert_state["alert_history"][-100:]
    record_activity("alert", f"Alert: {alert.get('message', alert.get('type', 'unknown'))}", alert)
    try:
        broadcast_alert(alert)
    except Exception:
        pass


def check_enhanced_alerts():
    """
     Enhanced alert checking with per-miner hashrate monitoring
    NOTE: This only tracks alerts for dashboard UI display.
    Spiral Sentinel handles all notifications (Discord/Telegram) and auto-restart.
    """
    global alert_state

    if not alert_config.get("enabled", True):
        return []

    alerts = []
    current_time = time.time()

    # Get current stats
    pool_hashrate_ths = miner_cache["totals"].get("hashrate_ths", 0)

    # Check minimum hashrate threshold
    min_hashrate = alert_config.get("hashrate_min_ths", 0)
    if min_hashrate > 0 and pool_hashrate_ths < min_hashrate:
        alert = {
            "type": "hashrate_low",
            "message": f"Pool hashrate ({pool_hashrate_ths:.2f} TH/s) below minimum threshold ({min_hashrate} TH/s)",
            "severity": "critical",
            "time": current_time
        }
        alerts.append(alert)

    # Check overall hashrate drop
    if alert_state["last_hashrate"] > 0 and pool_hashrate_ths > 0:
        drop_percent = ((alert_state["last_hashrate"] - pool_hashrate_ths) / alert_state["last_hashrate"]) * 100
        if drop_percent >= alert_config.get("hashrate_drop_percent", 50):
            alert = {
                "type": "hashrate_drop",
                "message": f"Pool hashrate dropped {drop_percent:.1f}% (from {alert_state['last_hashrate']:.2f} to {pool_hashrate_ths:.2f} TH/s)",
                "severity": "warning",
                "time": current_time
            }
            alerts.append(alert)

    if pool_hashrate_ths > 0:
        alert_state["last_hashrate"] = pool_hashrate_ths

    # Check per-miner hashrate drops - for UI display only
    for name, miner in miner_cache.get("miners", {}).items():
        if not miner.get("online", False):
            # Offline miner alert (UI display only - Sentinel handles notifications)
            last_seen = alert_state["miner_last_seen"].get(name, current_time)
            offline_minutes = (current_time - last_seen) / 60
            if offline_minutes >= alert_config.get("miner_offline_minutes", 5):
                alert = {
                    "type": "miner_offline",
                    "message": f"Miner '{name}' offline for {offline_minutes:.0f} minutes",
                    "miner": name,
                    "severity": "critical",
                    "time": current_time
                }
                alerts.append(alert)
        else:
            alert_state["miner_last_seen"][name] = current_time

            # Check per-miner hashrate drop (skip ESP32 — kH/s lottery miners fluctuate wildly)
            current_hashrate = miner.get("hashrate_ghs", 0)
            last_hashrate = alert_state["miner_last_hashrate"].get(name, 0)
            is_esp32_miner = miner.get("no_http_api", False)

            if last_hashrate > 0 and current_hashrate > 0 and not is_esp32_miner:
                drop_pct = ((last_hashrate - current_hashrate) / last_hashrate) * 100
                if drop_pct >= alert_config.get("miner_hashrate_drop_percent", 30):
                    alert = {
                        "type": "miner_hashrate_drop",
                        "message": f"Miner '{name}' hashrate dropped {drop_pct:.1f}%",
                        "miner": name,
                        "severity": "warning",
                        "time": current_time
                    }
                    alerts.append(alert)

            if current_hashrate > 0:
                alert_state["miner_last_hashrate"][name] = current_hashrate

            # Temperature alerts (UI display only, skip Avalon devices - personal heaters)
            miner_type_ui = miner.get("type", "")
            is_avalon_ui = miner_type_ui.lower().startswith("avalon") if miner_type_ui else False
            if not is_avalon_ui:
                temps = miner.get("temps", {})
                for temp_type, temp_value in temps.items():
                    if temp_value >= alert_config.get("temp_critical", 80):
                        alert = {
                            "type": "temperature_critical",
                            "message": f"CRITICAL: Miner '{name}' {temp_type} at {temp_value}°C",
                            "miner": name,
                            "severity": "critical",
                            "time": current_time
                        }
                        alerts.append(alert)
                    elif temp_value >= alert_config.get("temp_warning", 70):
                        alerts.append({
                            "type": "temperature_warning",
                            "message": f"Warning: Miner '{name}' {temp_type} at {temp_value}°C",
                            "miner": name,
                            "severity": "warning",
                            "time": current_time
                        })

    # Store alerts for UI
    alert_state["alerts_triggered"] = alerts

    # Keep alert history (last 100) for UI display
    for alert in alerts:
        log_alert_to_history(alert)

    return alerts


@app.route('/api/alerts/history', methods=['GET'])
@api_key_or_login_required
def get_alert_history():
    """ Get alert history"""
    return jsonify({
        "history": alert_state.get("alert_history", []),
        "current": alert_state.get("alerts_triggered", [])
    })


# ============================================
# V1.0: WEBHOOK VALIDATION AND TESTING
# Provides feedback for Discord/Telegram webhook configuration
# ============================================

def validate_discord_webhook_url(url: str) -> tuple:
    """
    Validate Discord webhook URL format and domain.
    Returns: (is_valid: bool, error_message: str or None, warnings: list)

    SECURITY: Only allows official Discord webhook domains to prevent SSRF.
    """
    warnings = []

    if not url:
        return False, "Webhook URL is empty", warnings

    if not isinstance(url, str):
        return False, "Webhook URL must be a string", warnings

    # Check for placeholder values
    if "YOUR" in url.upper() or "EXAMPLE" in url.upper():
        return False, "Webhook URL contains placeholder text - replace with your actual webhook", warnings

    # SECURITY: Must use HTTPS
    if not url.startswith("https://"):
        return False, "Webhook URL must use HTTPS", warnings

    # SECURITY: Only allow official Discord domains (SSRF prevention)
    valid_prefixes = [
        "https://discord.com/api/webhooks/",
        "https://discordapp.com/api/webhooks/",
        "https://canary.discord.com/api/webhooks/",
        "https://ptb.discord.com/api/webhooks/",
    ]

    if not any(url.startswith(prefix) for prefix in valid_prefixes):
        return False, "Invalid Discord webhook URL - must start with https://discord.com/api/webhooks/", warnings

    # Extract webhook ID and token
    parts = url.split("/api/webhooks/")
    if len(parts) != 2:
        return False, "Invalid webhook URL format", warnings

    webhook_parts = parts[1].strip("/").split("/")
    if len(webhook_parts) != 2:
        return False, "Webhook URL missing ID or token", warnings

    webhook_id, webhook_token = webhook_parts

    # Validate webhook ID (should be numeric)
    if not webhook_id.isdigit():
        return False, "Invalid webhook ID (must be numeric)", warnings

    # Validate token length (Discord tokens are typically 60-70 chars)
    if len(webhook_token) < 50:
        warnings.append("Webhook token seems short - verify it's complete")

    return True, None, warnings


def validate_telegram_config(bot_token: str, chat_id: str) -> tuple:
    """
    Validate Telegram bot token and chat ID format.
    Returns: (is_valid: bool, error_message: str or None, warnings: list)

    SECURITY: Only validates format, actual connectivity is tested separately.
    """
    import re
    warnings = []

    if not bot_token:
        return False, "Bot token is empty", warnings

    if not chat_id:
        return False, "Chat ID is empty", warnings

    # Validate bot token format: {bot_id}:{token}
    # Bot ID is numeric, token is alphanumeric with underscores
    token_pattern = r'^\d+:[A-Za-z0-9_-]{30,}$'
    if not re.match(token_pattern, bot_token):
        return False, "Invalid bot token format - should be like '123456789:ABC-DEF_ghi...'", warnings

    # Validate chat ID (numeric, can be negative for groups)
    chat_id_clean = chat_id.lstrip("-")
    if not chat_id_clean.isdigit():
        return False, "Invalid chat ID - must be numeric", warnings

    return True, None, warnings


def test_discord_webhook(url: str, test_message: str = None) -> dict:
    """
    Send a test message to a Discord webhook and return the result.
    Returns: {"success": bool, "message": str, "status_code": int}
    """
    import urllib.request
    import urllib.error

    # Validate first
    is_valid, error, warnings = validate_discord_webhook_url(url)
    if not is_valid:
        return {"success": False, "message": error, "warnings": warnings}

    # Build test embed
    embed = {
        "title": "🧪 Spiral Pool Test Notification",
        "description": test_message or "This is a test message from Spiral Dashboard. If you see this, your webhook is configured correctly!",
        "color": 0x00d4ff,  # Cyan color
        "footer": {"text": f"Spiral Pool v1.2.1 CONVERGENT SPIRAL"},
        "timestamp": datetime.now(timezone.utc).isoformat()
    }

    try:
        req = urllib.request.Request(
            url,
            data=json.dumps({"embeds": [embed]}).encode('utf-8'),
            headers={
                "Content-Type": "application/json",
                "User-Agent": "SpiralDashboard/1.0"
            }
        )

        with urllib.request.urlopen(req, timeout=10) as resp:
            if resp.status in [200, 204]:
                return {
                    "success": True,
                    "message": "Test message sent successfully! Check your Discord channel.",
                    "status_code": resp.status,
                    "warnings": warnings
                }
            else:
                return {
                    "success": False,
                    "message": f"Unexpected response status: {resp.status}",
                    "status_code": resp.status,
                    "warnings": warnings
                }
    except urllib.error.HTTPError as e:
        error_messages = {
            400: "Bad request - webhook URL may be malformed",
            401: "Unauthorized - webhook token is invalid",
            403: "Forbidden - webhook has been deleted or you lack permissions",
            404: "Not found - webhook does not exist or has been deleted",
            429: "Rate limited - too many requests, try again later"
        }
        return {
            "success": False,
            "message": error_messages.get(e.code, f"HTTP error: {e.code}"),
            "status_code": e.code,
            "warnings": warnings
        }
    except urllib.error.URLError as e:
        return {
            "success": False,
            "message": f"Network error: {str(e.reason)}",
            "warnings": warnings
        }
    except socket.timeout:
        return {
            "success": False,
            "message": "Connection timed out - check your internet connection",
            "warnings": warnings
        }
    except Exception as e:
        return {
            "success": False,
            "message": f"Error: {str(e)}",
            "warnings": warnings
        }


@app.route('/api/webhook/validate/discord', methods=['POST'])
@admin_required
def api_validate_discord_webhook():
    """Validate a Discord webhook URL without sending a test message."""
    data = request.json or {}
    url = data.get("url", "").strip()

    is_valid, error, warnings = validate_discord_webhook_url(url)

    return jsonify({
        "valid": is_valid,
        "error": error,
        "warnings": warnings
    })


@app.route('/api/webhook/test/discord', methods=['POST'])
@admin_required
def api_test_discord_webhook():
    """Test a Discord webhook by sending a test message."""
    data = request.json or {}
    url = data.get("url", "").strip()
    message = data.get("message", "")

    if not url:
        return jsonify({"success": False, "message": "Webhook URL is required"}), 400

    # Rate limit: Only allow one test per 5 seconds per IP
    client_ip = request.remote_addr or 'unknown'
    cache_key = f"webhook_test_{client_ip}"
    now = time.time()

    with _api_cache_lock:
        if cache_key in _api_cache:
            last_test = _api_cache[cache_key]
            if now - last_test < 5:
                return jsonify({
                    "success": False,
                    "message": f"Please wait {int(5 - (now - last_test))} seconds before testing again"
                }), 429

        _api_cache[cache_key] = now

    result = test_discord_webhook(url, message if message else None)

    app.logger.info(f"Discord webhook test from {client_ip}: {'success' if result['success'] else 'failed'}")

    return jsonify(result)


@app.route('/api/webhook/validate/telegram', methods=['POST'])
@admin_required
def api_validate_telegram():
    """Validate Telegram bot token and chat ID format."""
    data = request.json or {}
    bot_token = data.get("bot_token", "").strip()
    chat_id = data.get("chat_id", "").strip()

    is_valid, error, warnings = validate_telegram_config(bot_token, chat_id)

    return jsonify({
        "valid": is_valid,
        "error": error,
        "warnings": warnings
    })


# ============================================
#  REMOTE MINER CONTROL (CGMiner API)
# ============================================

def cgminer_command_v2(ip, port, command, parameter=""):
    """Send command to CGMiner API (V2 with parameter support)"""
    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return {"error": "Invalid or blocked IP address (SSRF protection)"}
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(10)
        sock.connect((ip, int(port)))

        if parameter:
            msg = json.dumps({"command": command, "parameter": parameter})
        else:
            msg = json.dumps({"command": command})

        sock.sendall(msg.encode())
        response = b""
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            response += chunk
            if b'\x00' in chunk:
                break

        sock.close()

        # Parse response (remove null bytes)
        response_str = response.replace(b'\x00', b'').decode('utf-8', errors='ignore')
        return json.loads(response_str)
    except Exception as e:
        return {"error": str(e)}


@app.route('/api/miner/cgminer/stats', methods=['POST'])
@api_key_or_login_required
def get_cgminer_stats():
    """ Get detailed CGMiner stats for ASIC miners"""
    data = request.json
    ip = data.get("ip", "")
    port = data.get("port", 4028)

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    # Get summary
    summary = cgminer_command(ip, port, "summary")

    # Get stats
    stats = cgminer_command(ip, port, "stats")

    # Get pools
    pools = cgminer_command(ip, port, "pools")

    # Get devs (device info)
    devs = cgminer_command(ip, port, "devs")

    return jsonify({
        "success": True,
        "ip": ip,
        "summary": summary,
        "stats": stats,
        "pools": pools,
        "devs": devs
    })


@app.route('/api/miner/cgminer/restart', methods=['POST'])
@api_key_or_login_required
def cgminer_restart():
    """ Restart ASIC miner via CGMiner API"""
    data = request.json
    ip = data.get("ip", "")
    port = data.get("port", 4028)
    device_type = data.get("type", "").lower()

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    # Antminers use 'restart' command
    if "antminer" in device_type:
        result = cgminer_command(ip, port, "restart")
        if "error" not in result:
            return jsonify({"success": True, "message": f"Restart sent to {ip}"})
        return jsonify({"success": False, "error": result.get("error")})

    # Whatsminers use different API
    if "whatsminer" in device_type:
        # Whatsminer uses btminerapi
        result = cgminer_command(ip, port, "reboot")
        if "error" not in result:
            return jsonify({"success": True, "message": f"Reboot sent to {ip}"})
        return jsonify({"success": False, "error": result.get("error")})

    # Generic restart attempt
    result = cgminer_command(ip, port, "restart")
    if "error" not in result:
        return jsonify({"success": True, "message": f"Restart command sent"})

    return jsonify({"success": False, "error": "Restart not supported for this device"})


@app.route('/api/miner/cgminer/pools', methods=['POST'])
@api_key_or_login_required
def cgminer_update_pool():
    """Disabled - configure miners directly via their web interface."""
    return jsonify({
        "success": False,
        "error": "Configure your miner directly via its web interface.",
        "disabled": True
    }), 403


@app.route('/api/miner/cgminer/fan', methods=['POST'])
@api_key_or_login_required
def cgminer_set_fan():
    """ Set fan speed on ASIC miner (Antminer)"""
    data = request.json
    ip = data.get("ip", "")
    port = data.get("port", 4028)
    fan_speed = data.get("fan_speed", 0)  # 0 = auto, otherwise percent

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    try:
        speed = int(fan_speed)
        if speed < 0 or speed > 100:
            return jsonify({"success": False, "error": "Fan speed must be 0-100 (0=auto)"})
    except ValueError:
        return jsonify({"success": False, "error": "Invalid fan speed"})

    # This varies by manufacturer - Antminer example
    if speed == 0:
        result = cgminer_command(ip, port, "ascset", "0,fan,auto")
    else:
        result = cgminer_command(ip, port, "ascset", f"0,fan,{speed}")

    if "error" in result:
        return jsonify({"success": False, "error": result.get("error")})

    return jsonify({"success": True, "message": f"Fan speed set to {speed}%" if speed else "Fan set to auto"})


@app.route('/api/miner/cgminer/frequency', methods=['POST'])
@api_key_or_login_required
def cgminer_set_frequency():
    """ Set mining frequency on ASIC miner"""
    data = request.json
    ip = data.get("ip", "")
    port = data.get("port", 4028)
    frequency = data.get("frequency", 0)

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    try:
        freq = int(frequency)
        if freq < 100 or freq > 1000:
            return jsonify({"success": False, "error": "Frequency must be 100-1000 MHz"})
    except ValueError:
        return jsonify({"success": False, "error": "Invalid frequency"})

    # Set frequency (varies by miner model)
    result = cgminer_command(ip, port, "ascset", f"0,freq,{freq}")

    if "error" in result:
        return jsonify({"success": False, "error": result.get("error")})

    return jsonify({"success": True, "message": f"Frequency set to {freq} MHz"})


@app.route('/api/miner/whatsminer/info', methods=['POST'])
@api_key_or_login_required
def whatsminer_info():
    """ Get Whatsminer detailed info via btminer API"""
    data = request.json
    ip = data.get("ip", "")
    port = data.get("port", 4028)

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    # Whatsminer uses slightly different commands
    summary = cgminer_command(ip, port, "summary")
    edevs = cgminer_command(ip, port, "edevs")  # Extended device info
    estatvs = cgminer_command(ip, port, "estatvs")  # Extended status

    return jsonify({
        "success": True,
        "ip": ip,
        "summary": summary,
        "edevs": edevs,
        "estatvs": estatvs
    })


# ============================================
#  AXEOS / NERDQAXE++ REMOTE CONTROL
# ============================================

def axeos_api_call(ip, endpoint, method="GET", data=None, timeout=10):
    """Make HTTP API call to AxeOS/NerdQAxe++ device"""
    try:
        url = f"http://{ip}{endpoint}"
        if method == "GET":
            response = requests.get(url, timeout=timeout)
        elif method == "POST":
            response = requests.post(url, json=data, timeout=timeout)
        elif method == "PATCH":
            response = requests.patch(url, json=data, timeout=timeout)
        else:
            return {"error": f"Unsupported method: {method}"}

        if response.status_code == 200:
            try:
                return response.json()
            except (ValueError, json.JSONDecodeError):
                return {"success": True, "message": response.text}
        else:
            return {"error": f"HTTP {response.status_code}: {response.text}"}
    except requests.exceptions.Timeout:
        return {"error": "Connection timeout"}
    except requests.exceptions.ConnectionError:
        return {"error": "Connection refused - device offline?"}
    except Exception as e:
        return {"error": str(e)}


@app.route('/api/miner/axeos/stats', methods=['POST'])
@api_key_or_login_required
def get_axeos_stats():
    """ Get detailed stats from AxeOS/NerdQAxe++ miner"""
    data = request.json
    ip = data.get("ip", "")

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    # Get system info from AxeOS API
    system_info = axeos_api_call(ip, "/api/system/info")

    # Get current status (standard AxeOS only — NMAxe doesn't have /api/system)
    is_nmaxe_device = (
        str(system_info.get('hwModel', '')).upper() == 'NMAXE' or
        isinstance(system_info.get('stratum'), dict)
    )
    status = system_info if is_nmaxe_device else axeos_api_call(ip, "/api/system")

    # Combine into unified response
    result = {
        "success": True,
        "ip": ip,
        "device_type": "AxeOS",
        "system_info": system_info,
        "status": status
    }

    # Extract key metrics — NMAxe uses different field names from standard AxeOS
    if not status.get("error"):
        if is_nmaxe_device:
            result["hashrate_ghs"] = status.get("hashRate", 0)
            result["temp"] = status.get("asicTemp", 0)
            fans_list = status.get('fans', [])
            result["fan_percent"] = fans_list[0].get('rpm', 0) if fans_list else 0
            result["voltage"] = status.get("vcoreActual", status.get("vcoreReq", 0))
            result["frequency"] = status.get("freqReq", 0)
            result["best_diff"] = status.get("bestDiffEver", "0")
            result["shares_accepted"] = status.get("sharesAccepted", 0)
            result["shares_rejected"] = status.get("sharesRejected", 0)
            result["uptime"] = status.get("uptimeSeconds", 0)
            result["power_watts"] = status.get("power", 0)
            result["efficiency"] = status.get("efficiency", 0)
        else:
            result["hashrate_ghs"] = status.get("hashRate", 0) / 1e9 if status.get("hashRate") else 0
            result["temp"] = status.get("temp", 0)
            # Use fanPercent if available, otherwise fanspeed (can't use `or` - 0 is valid)
            fan_pct = status.get("fanPercent")
            result["fan_percent"] = fan_pct if fan_pct is not None else status.get("fanspeed", 0)
            # Use coreVoltage if available, otherwise voltage (can't use `or` - 0 is valid)
            core_v = status.get("coreVoltage")
            result["voltage"] = core_v if core_v is not None else status.get("voltage", 0)
            result["frequency"] = status.get("frequency", 0)
            result["best_diff"] = status.get("bestDiff", "0")
            result["shares_accepted"] = status.get("sharesAccepted", 0)
            result["shares_rejected"] = status.get("sharesRejected", 0)
            result["uptime"] = status.get("uptimeSeconds", 0)
            result["power_watts"] = status.get("power", 0)
            result["efficiency"] = status.get("efficiency", 0)  # J/TH

    # Detect device type from system info
    if not system_info.get("error"):
        # NMAxe firmware exposes hwModel directly
        if is_nmaxe_device:
            result["device_type"] = "NMaxe"
            result["model"] = "NMaxe"
            result["version"] = system_info.get("fwVersion", "Unknown")
            stratum = system_info.get('stratum', {})
            result["pool_url"] = stratum.get('used', {}).get('url', '')
        else:
            hostname = (system_info.get("hostname") or "").lower()
            version = (system_info.get("version") or "").lower()
            board_version = (system_info.get("boardVersion") or system_info.get("board") or "").lower()
            asic_model = (system_info.get("ASICModel") or "").lower()
            # Check if stratum URL format gives us a hint - NerdQAxe uses hostname-only
            stratum_url = (system_info.get("stratumURL") or "").lower()
            is_hostname_only_stratum = stratum_url and not stratum_url.startswith("stratum")

            # BitAxe GT (Gamma Turbo) 801 detection - MUST check BEFORE NerdQAxe
            # GT uses BM1370 chips but is NOT a NerdQAxe device
            # GT has 2x BM1370 chips, ~2.15 TH/s, 43W
            if ('gt' in board_version or '801' in board_version or
                'turbo' in board_version or 'gamma turbo' in board_version or
                'gt' in hostname or '801' in hostname):
                result["device_type"] = "axeos"
                result["model"] = "BitAxe GT 801"
            # NerdQAxe++ detection
            # NerdQAxe++ uses hostname-only format (no stratum+tcp://)
            # Check multiple fields for better detection:
            # - boardVersion/hostname containing "nerd"
            # - Stratum URL being hostname-only (no protocol prefix)
            # Note: Do NOT use BM1370 chip alone - many non-NerdQAxe devices use BM1370
            elif ("nerdqaxe" in hostname or "nerdqaxe" in version or
                "nerd" in board_version or "nerdqaxe" in board_version or
                "nerdqaxe" in asic_model or "nerd" in asic_model or
                is_hostname_only_stratum):
                result["device_type"] = "nerdqaxe"  # Lowercase for API matching
                result["model"] = "NerdQAxe++"
            elif "nmaxe" in hostname or "nmaxe" in version:
                result["device_type"] = "NMaxe"
                result["model"] = "NMaxe"
            elif "gamma" in hostname or "gamma" in version:
                result["device_type"] = "BitAxe Gamma"
                result["model"] = "BitAxe Gamma"
            elif "supra" in hostname or "supra" in version:
                result["device_type"] = "BitAxe Supra"
                result["model"] = "BitAxe Supra"
            elif "ultra" in hostname or "ultra" in version:
                result["device_type"] = "BitAxe Ultra"
                result["model"] = "BitAxe Ultra"
            elif "hex" in hostname or "hex" in version:
                result["device_type"] = "BitAxe Hex"
                result["model"] = "BitAxe Hex"
            else:
                result["model"] = system_info.get("ASICModel", "Unknown AxeOS")

    return jsonify(result)


@app.route('/api/miner/axeos/restart', methods=['POST'])
@api_key_or_login_required
def axeos_restart():
    """ Restart AxeOS/NerdQAxe++ miner"""
    data = request.json
    ip = data.get("ip", "")

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    # AxeOS uses POST /api/system/restart
    result = axeos_api_call(ip, "/api/system/restart", method="POST")

    if result.get("error"):
        return jsonify({"success": False, "error": result.get("error")})

    return jsonify({"success": True, "message": f"Restart command sent to {ip}"})


@app.route('/api/miner/axeos/frequency', methods=['POST'])
@api_key_or_login_required
def axeos_set_frequency():
    """ Set mining frequency on AxeOS/NerdQAxe++ miner"""
    data = request.json
    ip = data.get("ip", "")
    frequency = data.get("frequency", 0)

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    try:
        freq = int(frequency)
        # AxeOS frequency ranges vary by device:
        # BitAxe Ultra/Gamma: 400-575 MHz typical
        # NerdQAxe++: 400-650 MHz typical
        if freq < 100 or freq > 800:
            return jsonify({"success": False, "error": "Frequency must be 100-800 MHz"})
    except ValueError:
        return jsonify({"success": False, "error": "Invalid frequency"})

    # AxeOS uses PATCH /api/system with frequency parameter
    result = axeos_api_call(ip, "/api/system", method="PATCH", data={"frequency": freq})

    if result.get("error"):
        return jsonify({"success": False, "error": result.get("error")})

    return jsonify({"success": True, "message": f"Frequency set to {freq} MHz"})


@app.route('/api/miner/axeos/voltage', methods=['POST'])
@api_key_or_login_required
def axeos_set_voltage():
    """ Set core voltage on AxeOS/NerdQAxe++ miner"""
    data = request.json
    ip = data.get("ip", "")
    voltage = data.get("voltage", 0)

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    try:
        mv = int(voltage)
        # AxeOS voltage in millivolts - typical range 1100-1300 mV
        if mv < 1000 or mv > 1500:
            return jsonify({"success": False, "error": "Voltage must be 1000-1500 mV"})
    except ValueError:
        return jsonify({"success": False, "error": "Invalid voltage"})

    # AxeOS uses PATCH /api/system with coreVoltage parameter
    result = axeos_api_call(ip, "/api/system", method="PATCH", data={"coreVoltage": mv})

    if result.get("error"):
        return jsonify({"success": False, "error": result.get("error")})

    return jsonify({"success": True, "message": f"Voltage set to {mv} mV"})


@app.route('/api/miner/axeos/fan', methods=['POST'])
@api_key_or_login_required
def axeos_set_fan():
    """ Set fan speed on AxeOS/NerdQAxe++ miner"""
    data = request.json
    ip = data.get("ip", "")
    fan_speed = data.get("fan_speed", 0)
    auto_fan = data.get("auto_fan", None)

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    payload = {}

    # Handle auto fan toggle
    if auto_fan is not None:
        payload["autofanspeed"] = auto_fan
        if auto_fan:
            result = axeos_api_call(ip, "/api/system", method="PATCH", data=payload)
            if result.get("error"):
                return jsonify({"success": False, "error": result.get("error")})
            return jsonify({"success": True, "message": "Auto fan enabled"})

    try:
        speed = int(fan_speed)
        if speed < 0 or speed > 100:
            return jsonify({"success": False, "error": "Fan speed must be 0-100%"})
    except ValueError:
        return jsonify({"success": False, "error": "Invalid fan speed"})

    # Set manual fan speed - disable auto first
    payload["autofanspeed"] = False
    payload["fanspeed"] = speed

    result = axeos_api_call(ip, "/api/system", method="PATCH", data=payload)

    if result.get("error"):
        return jsonify({"success": False, "error": result.get("error")})

    return jsonify({"success": True, "message": f"Fan speed set to {speed}%"})


@app.route('/api/miner/axeos/pool', methods=['POST'])
@api_key_or_login_required
def axeos_set_pool():
    """Disabled - configure miners directly via their web interface."""
    return jsonify({
        "success": False,
        "error": "Configure your miner directly via its web interface.",
        "disabled": True
    }), 403


@app.route('/api/miner/braiins/pool', methods=['POST'])
@api_key_or_login_required
def braiins_set_pool():
    """Disabled - configure miners directly via their web interface."""
    return jsonify({
        "success": False,
        "error": "Configure your miner directly via its web interface.",
        "disabled": True
    }), 403


@app.route('/api/miner/vnish/pool', methods=['POST'])
@api_key_or_login_required
def vnish_set_pool():
    """Disabled - configure miners directly via their web interface."""
    return jsonify({
        "success": False,
        "error": "Configure your miner directly via its web interface.",
        "disabled": True
    }), 403


@app.route('/api/miner/luxos/pool', methods=['POST'])
@api_key_or_login_required
def luxos_set_pool():
    """Disabled - configure miners directly via their web interface."""
    return jsonify({
        "success": False,
        "error": "Configure your miner directly via its web interface.",
        "disabled": True
    }), 403


@app.route('/api/miner/axeos/wifi', methods=['POST'])
@api_key_or_login_required
def axeos_set_wifi():
    """ Update WiFi configuration on AxeOS/NerdQAxe++ miner"""
    data = request.json
    ip = data.get("ip", "")
    ssid = data.get("ssid", "")
    wifi_password = data.get("password", "")

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    if not ssid:
        return jsonify({"success": False, "error": "SSID required"})

    # AxeOS uses PATCH /api/system with WiFi settings
    payload = {
        "wifiSSID": ssid,
        "wifiPassword": wifi_password
    }

    result = axeos_api_call(ip, "/api/system", method="PATCH", data=payload)

    if result.get("error"):
        return jsonify({"success": False, "error": result.get("error")})

    return jsonify({
        "success": True,
        "message": f"WiFi updated on {ip}. Restart required."
    })


@app.route('/api/miner/axeos/hostname', methods=['POST'])
@api_key_or_login_required
def axeos_set_hostname():
    """ Update hostname on AxeOS/NerdQAxe++ miner"""
    data = request.json
    ip = data.get("ip", "")
    hostname = data.get("hostname", "")

    if not ip:
        return jsonify({"success": False, "error": "IP required"})

    # SECURITY: Validate IP to prevent SSRF attacks
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP address - only private network IPs allowed"})

    if not hostname:
        return jsonify({"success": False, "error": "Hostname required"})

    # Validate hostname (alphanumeric and hyphens only)
    import re
    if not re.match(r'^[a-zA-Z0-9\-]+$', hostname):
        return jsonify({"success": False, "error": "Invalid hostname (alphanumeric and hyphens only)"})

    result = axeos_api_call(ip, "/api/system", method="PATCH", data={"hostname": hostname})

    if result.get("error"):
        return jsonify({"success": False, "error": result.get("error")})

    return jsonify({"success": True, "message": f"Hostname set to {hostname}"})


# ============================================
#  BLOCK FINDER ATTRIBUTION
# ============================================

# Track which miner found each block
block_finder_history = []

def record_block_finder(block_hash, block_height, worker_name, miner_ip=None, device_type=None):
    """Record which miner found a block for attribution"""
    global block_finder_history

    block_record = {
        "hash": block_hash,
        "height": block_height,
        "worker": worker_name,
        "miner_ip": miner_ip,
        "device_type": device_type,
        "found_at": datetime.utcnow().isoformat(),
        "timestamp": time.time()
    }

    # Add to history (keep last 100 blocks)
    block_finder_history.append(block_record)
    if len(block_finder_history) > 100:
        block_finder_history[:] = block_finder_history[-100:]

    # Save to persistent storage
    try:
        _atomic_json_save(os.path.join(CONFIG_DIR, "block_history.json"), block_finder_history, indent=2)
    except Exception as e:
        print(f"Error saving block history: {e}")

    return block_record


def load_block_finder_history():
    """Load block finder history from file"""
    global block_finder_history
    try:
        history_file = os.path.join(CONFIG_DIR, "block_history.json")
        if os.path.exists(history_file):
            with open(history_file, "r") as f:
                block_finder_history = json.load(f)
    except Exception as e:
        print(f"Error loading block history: {e}")
        block_finder_history = []


def get_miner_info_by_worker(worker_name):
    """Look up miner device info by worker name"""
    devices = load_config().get("devices", {})

    # Search through all device types
    for device_type in ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "avalon", "antminer", "antminer_scrypt", "whatsminer", "innosilicon", "goldshell", "hammer", "futurebit", "braiins", "vnish", "luxos", "luckyminer", "jingleminer", "zyber", "gekkoscience", "ipollo", "ebang", "epic", "elphapex", "canaan"]:
        for device in devices.get(device_type, []):
            device_name = device.get("name", "").lower()
            device_ip = device.get("ip", "")

            # Worker name often contains device name
            if device_name and device_name in worker_name.lower():
                return {
                    "ip": device_ip,
                    "name": device.get("name"),
                    "type": device_type,
                    "model": device.get("model", device_type)
                }

    return None


@app.route('/api/blocks/finder/<block_hash>', methods=['GET'])
@api_key_or_login_required
def get_block_finder(block_hash):
    """ Get which miner found a specific block"""
    global block_finder_history

    # SECURITY: Validate block hash format (64 hex characters)
    if not block_hash or not re.match(r'^[a-fA-F0-9]{64}$', block_hash):
        return jsonify({"success": False, "error": "Invalid block hash format"}), 400

    # Search local history first
    for block in block_finder_history:
        if block.get("hash") == block_hash:
            return jsonify({
                "success": True,
                "block": block
            })

    # If not in local history, check pool API
    try:
        resp = requests.get(f"{POOL_API_URL}/api/pools/{get_pool_id()}/blocks", timeout=5)
        if resp.status_code == 200:
            blocks = resp.json()
            for block in blocks:
                if block.get("hash") == block_hash:
                    worker = block.get("worker", "") or block.get("miner", "")
                    miner_info = get_miner_info_by_worker(worker)

                    result = {
                        "hash": block_hash,
                        "height": block.get("height", 0),
                        "worker": worker,
                        "reward": block.get("reward", 0),
                        "found_at": block.get("created", "")
                    }

                    if miner_info:
                        result["miner_ip"] = miner_info.get("ip")
                        result["device_type"] = miner_info.get("type")
                        result["device_name"] = miner_info.get("name")
                        result["model"] = miner_info.get("model")

                    return jsonify({"success": True, "block": result})
    except Exception as e:
        print(f"Error fetching block from pool: {e}")

    return jsonify({"success": False, "error": "Block not found"})


@app.route('/api/blocks/history', methods=['GET'])
@api_key_or_login_required
def get_block_history():
    """ Get block finder history with miner attribution"""
    global block_finder_history

    # Merge local history with pool API data
    enriched_history = []

    # Get latest from pool API
    try:
        resp = requests.get(f"{POOL_API_URL}/api/pools/{get_pool_id()}/blocks", timeout=5)
        if resp.status_code == 200:
            pool_blocks = resp.json()

            for block in pool_blocks[:50]:  # Last 50 blocks
                worker = block.get("worker", "") or block.get("miner", "")
                miner_info = get_miner_info_by_worker(worker)

                block_record = {
                    "hash": block.get("hash", ""),
                    "height": block.get("height", 0),
                    "worker": worker,
                    "reward": block.get("reward", 0),
                    "confirmations": block.get("confirmations", 0),
                    # P0 AUDIT FIX: Don't default to "confirmed" - use "unknown" if status missing
                    "status": block.get("status", "unknown"),
                    "found_at": block.get("created", ""),
                    "explorer_url": f"{DIGIEXPLORER_URL}/block/{block.get('hash', '')}"
                }

                # Add miner device info if found
                if miner_info:
                    block_record["miner_ip"] = miner_info.get("ip")
                    block_record["device_type"] = miner_info.get("type")
                    block_record["device_name"] = miner_info.get("name")
                    block_record["model"] = miner_info.get("model")

                enriched_history.append(block_record)
    except Exception as e:
        print(f"Error fetching blocks from pool: {e}")
        # Fall back to local history
        enriched_history = block_finder_history

    # Calculate per-miner stats
    miner_stats = {}
    for block in enriched_history:
        worker = block.get("worker", "unknown")
        if worker not in miner_stats:
            miner_stats[worker] = {
                "blocks_found": 0,
                "total_reward": 0,
                "device_type": block.get("device_type"),
                "device_name": block.get("device_name"),
                "last_block": None
            }
        miner_stats[worker]["blocks_found"] += 1
        miner_stats[worker]["total_reward"] += block.get("reward", 0)
        if not miner_stats[worker]["last_block"]:
            miner_stats[worker]["last_block"] = block.get("found_at")

    return jsonify({
        "success": True,
        "blocks": enriched_history,
        "total_blocks": len(enriched_history),
        "miner_stats": miner_stats
    })


@app.route('/api/blocks/leaderboard', methods=['GET'])
@api_key_or_login_required
def get_block_leaderboard():
    """ Get leaderboard of miners by blocks found (all coins)"""
    try:
        # Fetch blocks from all pools (multi-coin / merge-mining support)
        pool_blocks = []
        try:
            pools_resp = requests.get(f"{POOL_API_URL}/api/pools", timeout=5)
            if pools_resp.status_code == 200:
                pools = pools_resp.json().get("pools", [])
                for pool in pools:
                    pid = pool.get("id", "")
                    if not pid:
                        continue
                    try:
                        blk_resp = requests.get(f"{POOL_API_URL}/api/pools/{pid}/blocks", timeout=5)
                        if blk_resp.status_code == 200:
                            blocks = blk_resp.json()
                            coin = pool.get("coin", {}).get("type", "").upper()
                            for b in blocks:
                                b.setdefault("coin", coin)
                            pool_blocks.extend(blocks)
                    except Exception:
                        continue
        except Exception:
            pass

        # Fallback: if multi-pool fetch failed, try single pool
        if not pool_blocks:
            resp = requests.get(f"{POOL_API_URL}/api/pools/{get_pool_id()}/blocks", timeout=5)
            if resp.status_code != 200:
                return jsonify({"success": False, "error": "Failed to fetch blocks"})
            pool_blocks = resp.json()

        # Count blocks per miner, consolidating workers that map to the same device.
        # e.g. "HashForge" and "HashForge.worker1" both resolve to device "HashForge"
        # and should be merged into a single leaderboard entry.
        leaderboard = {}       # keyed by grouping key (device_name or worker)
        worker_to_key = {}     # cache: raw worker -> grouping key

        for block in pool_blocks:
            worker = block.get("source", "") or block.get("worker", "") or ""
            if not worker:
                continue

            # Determine grouping key: device_name if we can resolve it, else worker
            if worker not in worker_to_key:
                miner_info = get_miner_info_by_worker(worker)
                group_key = (miner_info.get("name") if miner_info else None) or worker
                worker_to_key[worker] = (group_key, miner_info)

            group_key, miner_info = worker_to_key[worker]

            if group_key not in leaderboard:
                leaderboard[group_key] = {
                    "worker": worker,
                    "blocks_found": 0,
                    "rewards_by_coin": {},
                    "first_block": None,
                    "last_block": None,
                    "device_type": miner_info.get("type") if miner_info else None,
                    "device_name": miner_info.get("name") if miner_info else None,
                    "model": miner_info.get("model") if miner_info else None
                }

            leaderboard[group_key]["blocks_found"] += 1
            coin = block.get("coin", "").upper() or "BTC"
            rewards = leaderboard[group_key]["rewards_by_coin"]
            rewards[coin] = rewards.get(coin, 0) + block.get("reward", 0)

            block_time = block.get("created", "")
            if not leaderboard[group_key]["first_block"] or block_time < leaderboard[group_key]["first_block"]:
                leaderboard[group_key]["first_block"] = block_time
            if not leaderboard[group_key]["last_block"] or block_time > leaderboard[group_key]["last_block"]:
                leaderboard[group_key]["last_block"] = block_time

        # Sort by blocks found (descending)
        sorted_leaderboard = sorted(
            leaderboard.values(),
            key=lambda x: x["blocks_found"],
            reverse=True
        )

        return jsonify({
            "success": True,
            "leaderboard": sorted_leaderboard,
            "total_miners": len(sorted_leaderboard),
            "total_blocks": len(pool_blocks)
        })

    except Exception as e:
        app.logger.error(f"Leaderboard error: {e}")
        return jsonify({"success": False, "error": "Failed to load leaderboard"})


# ============================================
#  FLEET MANAGEMENT
# ============================================

# Miner groups for fleet management
miner_groups = {}

# Maintenance mode state
maintenance_mode = {
    "enabled": False,
    "started_at": None,
    "reason": "",
    "paused_alerts": True
}


def load_miner_groups():
    """Load miner groups from config"""
    global miner_groups
    try:
        groups_file = os.path.join(CONFIG_DIR, "miner_groups.json")
        if os.path.exists(groups_file):
            with open(groups_file, "r") as f:
                miner_groups = json.load(f)
    except Exception as e:
        print(f"Error loading miner groups: {e}")
        miner_groups = {}


def save_miner_groups():
    """Save miner groups to config"""
    try:
        _atomic_json_save(os.path.join(CONFIG_DIR, "miner_groups.json"), miner_groups, indent=2)
    except Exception as e:
        print(f"Error saving miner groups: {e}")


@app.route('/api/fleet/groups', methods=['GET'])
@api_key_or_login_required
def get_miner_groups():
    """ Get all miner groups"""
    return jsonify({
        "success": True,
        "groups": miner_groups
    })


@app.route('/api/fleet/groups', methods=['POST'])
@admin_required
def create_miner_group():
    """ Create a new miner group"""
    data = request.json
    group_name = str(data.get("name", "")).strip()
    group_type = str(data.get("type", "location"))
    miners = data.get("miners", [])  # List of miner IPs or names

    if not group_name:
        return jsonify({"success": False, "error": "Group name required"})
    if len(group_name) > 64:
        return jsonify({"success": False, "error": "Group name too long (max 64 chars)"})
    # Only allow printable ASCII, no control characters
    import re
    if not re.match(r'^[\w\s\-\.()]+$', group_name):
        return jsonify({"success": False, "error": "Group name contains invalid characters"})
    valid_types = {"location", "model", "custom"}
    if group_type not in valid_types:
        return jsonify({"success": False, "error": f"Invalid group type. Must be one of: {', '.join(sorted(valid_types))}"})
    if not isinstance(miners, list) or len(miners) > 500:
        return jsonify({"success": False, "error": "miners must be a list (max 500 entries)"})
    # Validate each miner entry is a string of reasonable length
    miners = [str(m).strip()[:128] for m in miners if isinstance(m, (str, int, float))]

    miner_groups[group_name] = {
        "name": group_name,
        "type": group_type,
        "miners": miners,
        "created_at": datetime.utcnow().isoformat()
    }
    save_miner_groups()

    return jsonify({"success": True, "group": miner_groups[group_name]})


@app.route('/api/fleet/groups/<group_name>', methods=['PUT'])
@admin_required
def update_miner_group(group_name):
    """ Update a miner group"""
    if group_name not in miner_groups:
        return jsonify({"success": False, "error": "Group not found"})

    import re
    data = request.json
    if "miners" in data:
        miners = data["miners"]
        if not isinstance(miners, list) or len(miners) > 500:
            return jsonify({"success": False, "error": "miners must be a list (max 500 entries)"})
        miner_groups[group_name]["miners"] = [str(m).strip()[:128] for m in miners if isinstance(m, (str, int, float))]
    if "type" in data:
        valid_types = {"location", "model", "custom"}
        if str(data["type"]) not in valid_types:
            return jsonify({"success": False, "error": f"Invalid group type. Must be one of: {', '.join(sorted(valid_types))}"})
        miner_groups[group_name]["type"] = str(data["type"])
    if "name" in data and data["name"] != group_name:
        new_name = str(data["name"]).strip()
        if not new_name or len(new_name) > 64 or not re.match(r'^[\w\s\-\.()]+$', new_name):
            return jsonify({"success": False, "error": "Invalid new group name"})
        # Rename group
        miner_groups[new_name] = miner_groups.pop(group_name)
        miner_groups[new_name]["name"] = new_name

    save_miner_groups()
    return jsonify({"success": True, "group": miner_groups.get(data.get("name", group_name))})


@app.route('/api/fleet/groups/<group_name>', methods=['DELETE'])
@admin_required
def delete_miner_group(group_name):
    """ Delete a miner group"""
    if group_name not in miner_groups:
        return jsonify({"success": False, "error": "Group not found"})

    del miner_groups[group_name]
    save_miner_groups()
    return jsonify({"success": True, "message": f"Group '{group_name}' deleted"})


# ============================================
# AVALON POWER SCHEDULING
# ============================================
# Allows scheduling different work profiles (efficiency, balanced, high)
# based on time of day - useful for electricity rate optimization

# Avalon profile presets by model
# Frequency (MHz), voltage, and workmode settings based on:
# - Canaan CGMiner documentation: https://github.com/Canaan-Creative/cgminer
# - Canaan Avalon API docs: https://github.com/Canaan-Creative/avalon10-docs
# - Device specifications from official Canaan sources
#
# Workmode API: ascset|0,workmode,<mode> where mode is 0=normal, 1=high, 255=query
# Frequency API: ascset|0,freq,<MHz>
# Voltage API: ascset|0,voltage,1-<value>
AVALON_PROFILES = {
    # ═══════════════════════════════════════════════════════════════════════════
    # AVALON NANO SERIES (Home/Desktop miners, voltage range ~5000-9000)
    # ═══════════════════════════════════════════════════════════════════════════

    # Avalon Nano 3 - Desktop miner (4 TH/s max, 140W)
    # Specs: 3 TH/s @ 70W (low), 4.5 TH/s @ 100W (med), 6 TH/s @ 140W (high)
    "nano3": {
        "efficiency": {"freq": 200, "voltage": 6000, "workmode": 0, "description": "~3 TH/s @ 70W, quiet"},
        "balanced": {"freq": 300, "voltage": 6500, "workmode": 0, "description": "~4.5 TH/s @ 100W"},
        "high": {"freq": 400, "voltage": 7000, "workmode": 1, "description": "~6 TH/s @ 140W"},
    },

    # Avalon Nano 3S - Desktop miner (6 TH/s max, 140W)
    # Same chipset as Nano 3 but factory tuned higher
    "nano3s": {
        "efficiency": {"freq": 200, "voltage": 6000, "workmode": 0, "description": "~3 TH/s @ 70W, quiet"},
        "balanced": {"freq": 300, "voltage": 6500, "workmode": 0, "description": "~4.5 TH/s @ 100W"},
        "high": {"freq": 400, "voltage": 7500, "workmode": 1, "description": "~6 TH/s @ 140W"},
    },

    # ═══════════════════════════════════════════════════════════════════════════
    # AVALON Q SERIES (Home miners with Eco/Standard/Super modes)
    # These use workmode switching rather than freq/voltage directly
    # ═══════════════════════════════════════════════════════════════════════════

    # Avalon Q - Quiet home miner (90 TH/s max, 1800W)
    # Eco: 55 TH/s @ 875W, Standard: 80 TH/s @ 1450W, Super: 90 TH/s @ 1800W
    "avalon_q": {
        "efficiency": {"freq": 0, "voltage": 0, "workmode": 0, "description": "Eco: ~55 TH/s @ 875W, <40dB"},
        "balanced": {"freq": 0, "voltage": 0, "workmode": 1, "description": "Standard: ~80 TH/s @ 1450W"},
        "high": {"freq": 0, "voltage": 0, "workmode": 2, "description": "Super: ~90 TH/s @ 1800W"},
    },

    # ═══════════════════════════════════════════════════════════════════════════
    # AVALON 7/8 SERIES (Older generation, freq range: AV7 24-1404MHz, AV8 25-1200MHz)
    # Uses avalon7/8 specific voltage levels via CGMiner
    # ═══════════════════════════════════════════════════════════════════════════

    # Avalon 7 - freq 24-1404 MHz (step 12), voltage via level/offset
    "avalon7": {
        "efficiency": {"freq": 400, "voltage": 0, "workmode": 0, "description": "Low freq, reduced power"},
        "balanced": {"freq": 500, "voltage": 0, "workmode": 0, "description": "Standard operation"},
        "high": {"freq": 650, "voltage": 0, "workmode": 1, "description": "High performance"},
    },

    # Avalon 8 - freq 25-1200 MHz (step 25)
    "avalon8": {
        "efficiency": {"freq": 600, "voltage": 0, "workmode": 0, "description": "Efficiency mode"},
        "balanced": {"freq": 800, "voltage": 0, "workmode": 0, "description": "Standard: ~800 MHz"},
        "high": {"freq": 1000, "voltage": 0, "workmode": 1, "description": "Performance mode"},
    },

    # ═══════════════════════════════════════════════════════════════════════════
    # AVALON 10xx SERIES (A10, uses workmode 0=31T normal, 1=37T performance)
    # API: ascset|0,workmode,<0|1>
    # ═══════════════════════════════════════════════════════════════════════════

    # Avalon A1066 (A10) - 50 TH/s max
    "a1066": {
        "efficiency": {"freq": 0, "voltage": 0, "workmode": 0, "description": "Normal: ~31 TH/s"},
        "balanced": {"freq": 0, "voltage": 0, "workmode": 0, "description": "Normal: ~31 TH/s"},
        "high": {"freq": 0, "voltage": 0, "workmode": 1, "description": "Performance: ~37 TH/s"},
    },

    # ═══════════════════════════════════════════════════════════════════════════
    # AVALON 11xx/12xx/13xx/14xx/15xx SERIES (Modern ASICs)
    # These typically use workmode for major changes, freq for fine-tuning
    # ═══════════════════════════════════════════════════════════════════════════

    # Avalon A1166 Pro (A11) - 81 TH/s @ 3400W
    "a1166": {
        "efficiency": {"freq": 550, "voltage": 0, "workmode": 0, "description": "Low power mode"},
        "balanced": {"freq": 600, "voltage": 0, "workmode": 0, "description": "Normal operation"},
        "high": {"freq": 650, "voltage": 0, "workmode": 1, "description": "High performance"},
    },

    # Avalon A1246 (A12) - 90 TH/s @ 3420W
    "a1246": {
        "efficiency": {"freq": 580, "voltage": 0, "workmode": 0, "description": "~75 TH/s, reduced power"},
        "balanced": {"freq": 640, "voltage": 0, "workmode": 0, "description": "~85 TH/s, normal"},
        "high": {"freq": 700, "voltage": 0, "workmode": 1, "description": "~90 TH/s, max"},
    },

    # Avalon A1346 (A13) - 110 TH/s @ 3300W
    "a1346": {
        "efficiency": {"freq": 550, "voltage": 0, "workmode": 0, "description": "~90 TH/s, efficiency"},
        "balanced": {"freq": 600, "voltage": 0, "workmode": 0, "description": "~100 TH/s, normal"},
        "high": {"freq": 650, "voltage": 0, "workmode": 1, "description": "~110 TH/s, max"},
    },

    # Avalon A1366 - 130 TH/s @ 3250W (25 J/TH)
    "a1366": {
        "efficiency": {"freq": 525, "voltage": 0, "workmode": 0, "description": "~105 TH/s, efficiency"},
        "balanced": {"freq": 575, "voltage": 0, "workmode": 0, "description": "~120 TH/s, normal"},
        "high": {"freq": 625, "voltage": 0, "workmode": 1, "description": "~130 TH/s, max"},
    },

    # Avalon A1466 - 150 TH/s @ 3230W (21.5 J/TH)
    "a1466": {
        "efficiency": {"freq": 550, "voltage": 0, "workmode": 0, "description": "~125 TH/s, efficiency"},
        "balanced": {"freq": 600, "voltage": 0, "workmode": 0, "description": "~140 TH/s, normal"},
        "high": {"freq": 650, "voltage": 0, "workmode": 1, "description": "~150 TH/s, max"},
    },

    # Avalon A1566 - 185 TH/s @ 3420W (18.5 J/TH)
    "a1566": {
        "efficiency": {"freq": 550, "voltage": 0, "workmode": 0, "description": "~155 TH/s, efficiency"},
        "balanced": {"freq": 600, "voltage": 0, "workmode": 0, "description": "~175 TH/s, normal"},
        "high": {"freq": 650, "voltage": 0, "workmode": 1, "description": "~185 TH/s, max"},
    },

    # ═══════════════════════════════════════════════════════════════════════════
    # GENERIC / UNKNOWN MODELS
    # Conservative defaults using CGMiner standard freq/voltage ranges
    # ═══════════════════════════════════════════════════════════════════════════
    "generic": {
        "efficiency": {"freq": 200, "voltage": 6000, "workmode": 0, "description": "Low power (conservative)"},
        "balanced": {"freq": 300, "voltage": 6500, "workmode": 0, "description": "Normal (conservative)"},
        "high": {"freq": 400, "voltage": 7000, "workmode": 1, "description": "High (conservative)"},
    },
}

# In-memory schedule storage (persisted to disk)
avalon_schedules = {}
_schedule_worker_running = False
_schedule_worker_thread = None


def load_avalon_schedules():
    """Load Avalon power schedules from config file"""
    global avalon_schedules
    try:
        schedules_file = os.path.join(CONFIG_DIR, "avalon_schedules.json")
        if os.path.exists(schedules_file):
            with open(schedules_file, "r") as f:
                avalon_schedules = json.load(f)
                print(f"[SCHEDULE] Loaded {len(avalon_schedules)} Avalon schedules")
    except Exception as e:
        print(f"[SCHEDULE] Error loading schedules: {e}")
        avalon_schedules = {}


def save_avalon_schedules():
    """Save Avalon power schedules to config file"""
    try:
        schedules_file = os.path.join(CONFIG_DIR, "avalon_schedules.json")
        _atomic_json_save(schedules_file, avalon_schedules, indent=2)
    except Exception as e:
        print(f"[SCHEDULE] Error saving schedules: {e}")


def get_active_profile_for_time(schedule, current_time):
    """
    Determine which profile should be active based on current time.

    Args:
        schedule: Schedule dict with 'rules' list
        current_time: datetime.time object

    Returns:
        Profile name (efficiency, balanced, high) or None if no rule matches
    """
    if not schedule.get("enabled", False):
        return None

    rules = schedule.get("rules", [])
    current_minutes = current_time.hour * 60 + current_time.minute

    for rule in rules:
        start_parts = rule.get("start", "00:00").split(":")
        end_parts = rule.get("end", "00:00").split(":")

        start_minutes = int(start_parts[0]) * 60 + int(start_parts[1])
        end_minutes = int(end_parts[0]) * 60 + int(end_parts[1])

        # Handle overnight ranges (e.g., 21:00 - 09:00)
        if start_minutes <= end_minutes:
            # Normal range (e.g., 09:00 - 21:00)
            if start_minutes <= current_minutes < end_minutes:
                return rule.get("profile", "balanced")
        else:
            # Overnight range (e.g., 21:00 - 09:00)
            if current_minutes >= start_minutes or current_minutes < end_minutes:
                return rule.get("profile", "balanced")

    return None


def apply_avalon_profile(ip, profile_name, model="generic"):
    """
    Apply a power profile to an Avalon miner.

    Uses CGMiner API commands:
    - ascset|0,workmode,<mode> - Switch work mode (0=eco/normal, 1=standard, 2=super/high)
    - ascset|0,freq,<MHz> - Set frequency (if supported by model)
    - ascset|0,voltage,1-<value> - Set voltage (if supported by model)

    Args:
        ip: Miner IP address
        profile_name: One of 'efficiency', 'balanced', 'high'
        model: Avalon model for profile lookup

    Returns:
        dict with success status and message
    """
    # Get profile settings for this model
    model_profiles = AVALON_PROFILES.get(model.lower(), AVALON_PROFILES["generic"])
    profile = model_profiles.get(profile_name)

    if not profile:
        return {"success": False, "error": f"Unknown profile: {profile_name}"}

    freq = profile.get("freq", 0)
    voltage = profile.get("voltage", 0)
    workmode = profile.get("workmode", 0)
    description = profile.get("description", "")

    errors = []
    applied = []

    try:
        # Apply workmode first (most important for many Avalon models)
        # This controls the major power/performance mode
        if workmode is not None:
            workmode_result = cgminer_command(ip, 4028, "ascset", f"0,workmode,{workmode}", timeout=5)
            if "error" in workmode_result and "invalid" not in str(workmode_result.get("error", "")).lower():
                errors.append(f"Workmode: {workmode_result.get('error')}")
            else:
                applied.append(f"workmode={workmode}")

        # Apply frequency (if non-zero - some models don't support direct freq control)
        if freq > 0:
            freq_result = cgminer_command(ip, 4028, "ascset", f"0,freq,{freq}", timeout=5)
            if "error" in freq_result and "invalid" not in str(freq_result.get("error", "")).lower():
                errors.append(f"Frequency: {freq_result.get('error')}")
            else:
                applied.append(f"freq={freq}MHz")

        # Apply voltage (if non-zero - some models don't support direct voltage control)
        if voltage > 0:
            volt_result = cgminer_command(ip, 4028, "ascset", f"0,voltage,1-{voltage}", timeout=5)
            if "error" in volt_result and "invalid" not in str(volt_result.get("error", "")).lower():
                errors.append(f"Voltage: {volt_result.get('error')}")
            else:
                applied.append(f"voltage={voltage}")

        # If we have errors but also applied some settings, report partial success
        if errors and applied:
            return {
                "success": True,
                "message": f"Applied {profile_name} ({', '.join(applied)}). Some settings not supported: {'; '.join(errors)}",
                "profile": profile_name,
                "description": description,
                "partial": True
            }
        elif errors:
            return {"success": False, "error": "; ".join(errors)}

        return {
            "success": True,
            "message": f"Applied {profile_name} profile: {', '.join(applied) if applied else 'workmode only'}",
            "profile": profile_name,
            "description": description,
            "freq": freq if freq > 0 else None,
            "voltage": voltage if voltage > 0 else None,
            "workmode": workmode
        }
    except Exception as e:
        return {"success": False, "error": str(e)}


def schedule_worker():
    """
    Background worker that checks schedules every minute and applies profiles.
    Runs in a separate thread.
    """
    global _schedule_worker_running

    print("[SCHEDULE] Power schedule worker started")
    last_applied = {}  # Track last applied profile per IP to avoid redundant commands

    while _schedule_worker_running:
        try:
            now = datetime.now()
            current_time = now.time()

            for ip, schedule in avalon_schedules.items():
                if not schedule.get("enabled", False):
                    continue

                # Determine which profile should be active
                target_profile = get_active_profile_for_time(schedule, current_time)

                if target_profile and last_applied.get(ip) != target_profile:
                    # Profile changed, apply it
                    model = schedule.get("model", "generic")
                    result = apply_avalon_profile(ip, target_profile, model)

                    if result.get("success"):
                        last_applied[ip] = target_profile
                        print(f"[SCHEDULE] {ip}: Applied {target_profile} profile")
                    else:
                        print(f"[SCHEDULE] {ip}: Failed to apply {target_profile} - {result.get('error')}")

        except Exception as e:
            print(f"[SCHEDULE] Worker error: {e}")

        # Check every 60 seconds
        time.sleep(60)

    print("[SCHEDULE] Power schedule worker stopped")


def start_schedule_worker():
    """Start the background schedule worker thread"""
    global _schedule_worker_running, _schedule_worker_thread

    if _schedule_worker_running:
        return  # Already running

    _schedule_worker_running = True
    _schedule_worker_thread = threading.Thread(target=schedule_worker, daemon=True)
    _schedule_worker_thread.start()


def stop_schedule_worker():
    """Stop the background schedule worker thread"""
    global _schedule_worker_running
    _schedule_worker_running = False


# --- Avalon Schedule API Endpoints ---

@app.route('/api/avalon/profiles', methods=['GET'])
@api_key_or_login_required
def get_avalon_profiles():
    """Get available Avalon power profiles"""
    return jsonify({
        "success": True,
        "profiles": AVALON_PROFILES
    })


@app.route('/api/avalon/schedules', methods=['GET'])
@api_key_or_login_required
def get_avalon_schedules():
    """Get all Avalon power schedules"""
    return jsonify({
        "success": True,
        "schedules": avalon_schedules
    })


@app.route('/api/avalon/schedules/<ip>', methods=['GET'])
@api_key_or_login_required
def get_avalon_schedule(ip):
    """Get schedule for a specific Avalon miner"""
    if ip not in avalon_schedules:
        return jsonify({
            "success": True,
            "schedule": {
                "enabled": False,
                "model": "generic",
                "rules": []
            }
        })
    return jsonify({
        "success": True,
        "schedule": avalon_schedules[ip]
    })


@app.route('/api/avalon/schedules/<ip>', methods=['POST', 'PUT'])
@admin_required
def set_avalon_schedule(ip):
    """
    Create or update schedule for an Avalon miner.

    Request body:
    {
        "enabled": true,
        "model": "nano3",  // nano3, a1246, a1366, generic
        "rules": [
            {"start": "09:00", "end": "21:00", "profile": "efficiency"},
            {"start": "21:00", "end": "09:00", "profile": "high"}
        ]
    }
    """
    # SECURITY: Validate IP
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP - only private network IPs allowed"})

    data = request.json

    # Validate rules
    rules = data.get("rules", [])
    valid_profiles = ["efficiency", "balanced", "high"]

    for rule in rules:
        if rule.get("profile") not in valid_profiles:
            return jsonify({"success": False, "error": f"Invalid profile: {rule.get('profile')}"})

        # Validate time format (HH:MM)
        for time_field in ["start", "end"]:
            time_str = rule.get(time_field, "")
            if not re.match(r'^([01]?[0-9]|2[0-3]):[0-5][0-9]$', time_str):
                return jsonify({"success": False, "error": f"Invalid time format for {time_field}: {time_str}"})

    avalon_schedules[ip] = {
        "enabled": data.get("enabled", False),
        "model": data.get("model", "generic"),
        "rules": rules,
        "updated_at": datetime.utcnow().isoformat()
    }

    save_avalon_schedules()

    # Ensure schedule worker is running if we have any enabled schedules
    if any(s.get("enabled") for s in avalon_schedules.values()):
        start_schedule_worker()

    return jsonify({
        "success": True,
        "message": f"Schedule saved for {ip}",
        "schedule": avalon_schedules[ip]
    })


@app.route('/api/avalon/schedules/<ip>', methods=['DELETE'])
@admin_required
def delete_avalon_schedule(ip):
    """Delete schedule for an Avalon miner"""
    # SECURITY: Validate IP (consistent with PUT handler)
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP"})
    if ip not in avalon_schedules:
        return jsonify({"success": False, "error": "Schedule not found"})

    del avalon_schedules[ip]
    save_avalon_schedules()

    return jsonify({"success": True, "message": f"Schedule deleted for {ip}"})


@app.route('/api/avalon/schedules/<ip>/apply', methods=['POST'])
@admin_required
def apply_profile_now(ip):
    """
    Manually apply a profile to an Avalon miner right now.

    Request body:
    {
        "profile": "efficiency"  // efficiency, balanced, high
    }
    """
    # SECURITY: Validate IP
    if not validate_miner_ip(ip):
        return jsonify({"success": False, "error": "Invalid IP - only private network IPs allowed"})

    data = request.json
    profile_name = data.get("profile", "balanced")

    # Get model from schedule if exists, otherwise use generic
    model = "generic"
    if ip in avalon_schedules:
        model = avalon_schedules[ip].get("model", "generic")

    result = apply_avalon_profile(ip, profile_name, model)
    return jsonify(result)


@app.route('/api/fleet/batch/restart', methods=['POST'])
@admin_required
def batch_restart_miners():
    """ Restart multiple miners at once"""
    data = request.json
    targets = data.get("targets", [])  # List of {ip, type} or group name
    group_name = data.get("group", "")

    # If group specified, get miners from group
    if group_name and group_name in miner_groups:
        targets = []
        devices = load_config().get("devices", {})
        for miner_id in miner_groups[group_name].get("miners", []):
            # Find miner in devices
            for device_type in ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "avalon", "antminer", "antminer_scrypt", "whatsminer", "innosilicon", "goldshell", "hammer", "futurebit", "braiins", "vnish", "luxos", "luckyminer", "jingleminer", "zyber", "gekkoscience", "ipollo", "ebang", "epic", "elphapex", "canaan"]:
                for device in devices.get(device_type, []):
                    if device.get("ip") == miner_id or device.get("name") == miner_id:
                        targets.append({"ip": device["ip"], "type": device_type})

    results = []
    for target in targets:
        ip = target.get("ip", "")
        miner_type = target.get("type", "axeos").lower()

        # SECURITY: Validate each IP to prevent SSRF attacks
        if not validate_miner_ip(ip):
            results.append({"ip": ip, "success": False, "message": "Invalid IP - only private network IPs allowed"})
            continue

        try:
            # AxeOS-based devices use HTTP restart API
            if miner_type in ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "bitaxe", "hammer", "luckyminer", "jingleminer", "zyber"]:
                result = axeos_api_call(ip, "/api/system/restart", method="POST")
            else:
                # CGMiner-based devices use CGMiner restart command
                result = cgminer_command(ip, 4028, "restart")

            results.append({
                "ip": ip,
                "success": "error" not in result,
                "message": result.get("error", "Restart sent")
            })
        except Exception as e:
            results.append({"ip": ip, "success": False, "message": str(e)})

    success_count = sum(1 for r in results if r["success"])
    return jsonify({
        "success": True,
        "total": len(results),
        "successful": success_count,
        "failed": len(results) - success_count,
        "results": results
    })


@app.route('/api/fleet/batch/pool', methods=['POST'])
@admin_required
def batch_update_pool():
    """ Update pool configuration on multiple miners.

    Supported miner types:
    - AxeOS/BitAxe/NerdQAxe: HTTP API PATCH to /api/system
    - Avalon: CGMiner API addpool + switchpool on port 4028
    - Antminer S19/S21/T21: CGMiner API addpool + switchpool on port 4028
    - Whatsminer M30/M50/M60: BTMiner API addpool + switchpool on port 4028

    Request body:
    {
        "pool_url": "192.168.1.100",      // Pool hostname/IP (required)
        "pool_port": 3333,                 // Pool port (default: coin-specific)
        "worker_prefix": "farm1",          // Optional prefix for worker names
        "password": "x",                   // Pool password (default: "x")
        "targets": [                       // List of miners to update
            {"ip": "192.168.1.50", "type": "axeos", "name": "bitaxe-01"},
            {"ip": "192.168.1.51", "type": "avalon", "name": "avalon-01"},
            {"ip": "192.168.1.52", "type": "antminer", "name": "s19-01"},
            {"ip": "192.168.1.53", "type": "whatsminer", "name": "m50-01"}
        ],
        "group": "all-miners"              // OR specify a group name instead of targets
    }
    """
    data = request.json
    targets = data.get("targets", [])
    group_name = data.get("group", "")
    pool_url = data.get("pool_url", "")
    # Get default port based on primary coin (alphabetical order)
    primary = get_primary_coin()
    default_port = {"BC2": 6333, "BCH": 5333, "BTC": 4333, "CAT": 12335,
                    "DGB": 3333, "DGB-SCRYPT": 3336, "DOGE": 8335,
                    "FBTC": 18335, "LTC": 7333, "NMC": 14335,
                    "PEP": 10335, "QBX": 20335, "SYS": 15335, "XMY": 17335}.get(primary, 3333) if primary else 3333
    pool_port = data.get("pool_port", default_port)
    worker_prefix = data.get("worker_prefix", "")
    password = data.get("password", "x")

    if not pool_url:
        return jsonify({"success": False, "error": "pool_url required"})

    # If group specified, get miners from group
    if group_name and group_name in miner_groups:
        targets = []
        devices = load_config().get("devices", {})
        for miner_id in miner_groups[group_name].get("miners", []):
            for device_type in ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "avalon", "antminer", "antminer_scrypt", "whatsminer", "innosilicon", "goldshell", "hammer", "futurebit", "braiins", "vnish", "luxos", "luckyminer", "jingleminer", "zyber", "gekkoscience", "ipollo", "ebang", "epic", "elphapex", "canaan"]:
                for device in devices.get(device_type, []):
                    if device.get("ip") == miner_id or device.get("name") == miner_id:
                        targets.append({
                            "ip": device["ip"],
                            "type": device_type,
                            "name": device.get("name", device["ip"])
                        })

    results = []
    for target in targets:
        ip = target.get("ip", "")
        miner_type = target.get("type", "axeos").lower()
        miner_name = target.get("name", ip)

        # SECURITY: Validate each IP to prevent SSRF attacks
        if not validate_miner_ip(ip):
            results.append({"ip": ip, "name": miner_name, "success": False, "message": "Invalid IP - only private network IPs allowed"})
            continue

        # Build worker name with prefix
        worker = f"{worker_prefix}.{miner_name}" if worker_prefix else miner_name

        try:
            if miner_type in ["axeos", "nmaxe", "bitaxe", "nerdqaxe", "nerdqaxe++", "nerdaxe", "esp32miner", "qaxe", "qaxeplus", "hammer", "luckyminer", "jingleminer", "zyber"]:
                # AxeOS-based miners use HTTP API
                # Normalize URL to just hostname
                stratum_host = pool_url.strip()
                for prefix in ['stratum+tcp://', 'stratum+ssl://', 'stratum://', 'tcp://', 'ssl://', 'http://', 'https://']:
                    if stratum_host.lower().startswith(prefix):
                        stratum_host = stratum_host[len(prefix):]
                        break
                stratum_host = stratum_host.rstrip('/')
                if ':' in stratum_host:
                    stratum_host = stratum_host.rsplit(':', 1)[0]

                # CRITICAL: Device-specific URL format
                # NMaxe/BitAxe: Expects FULL stratum URL with protocol
                # NerdQAxe: Expects ONLY the hostname (no protocol)
                is_nerdqaxe = miner_type in ["nerdqaxe", "nerdqaxe++", "nerdaxe"]

                # Auto-detect NerdQAxe if not already identified (handles misconfig)
                if not is_nerdqaxe:
                    try:
                        system_info = axeos_api_call(ip, "/api/system/info")
                        if not system_info.get("error"):
                            hostname = (system_info.get("hostname") or "").lower()
                            version = (system_info.get("version") or "").lower()
                            board_version = (system_info.get("boardVersion") or system_info.get("board") or "").lower()
                            asic_model = (system_info.get("ASICModel") or "").lower()
                            stratum_url_check = (system_info.get("stratumURL") or "").lower()
                            is_hostname_only_stratum = stratum_url_check and not stratum_url_check.startswith("stratum")
                            # Note: Do NOT use BM1370 chip alone for detection - many non-NerdQAxe devices use BM1370
                            # (BitAxe GT 801, Zyber 8G, etc.) and would be misconfigured

                            if ("nerdqaxe" in hostname or "nerdqaxe" in version or
                                "nerd" in board_version or "nerdqaxe" in board_version or
                                "nerdqaxe" in asic_model or "nerd" in asic_model or
                                is_hostname_only_stratum):
                                is_nerdqaxe = True
                                app.logger.info(f"Auto-detected NerdQAxe device at {ip} in bulk config")
                    except Exception:
                        pass

                if is_nerdqaxe:
                    stratum_url = stratum_host
                else:
                    stratum_url = f"stratum+tcp://{stratum_host}:{pool_port}"

                payload = {
                    "stratumURL": stratum_url,
                    "stratumPort": int(pool_port),
                    "stratumUser": worker,
                    "stratumPassword": password
                }
                result = axeos_api_call(ip, "/api/system", method="PATCH", data=payload)
                success = "error" not in result
                message = result.get("error", "Pool configured via HTTP API")

            elif miner_type == "goldshell":
                # Goldshell miners use HTTP API for pool configuration
                # API endpoint: /mcb/pools with POST data
                try:
                    pool_data = {
                        "pool1url": f"{pool_url}:{pool_port}",
                        "pool1user": worker,
                        "pool1pass": password
                    }
                    response = requests.post(f"http://{ip}/mcb/pools", json=pool_data, timeout=10)
                    if response.status_code == 200:
                        success = True
                        message = "Pool configured via Goldshell HTTP API"
                    else:
                        success = False
                        message = f"Goldshell API returned status {response.status_code}"
                except requests.exceptions.RequestException as e:
                    success = False
                    message = f"Goldshell API error: {str(e)}"

            elif miner_type == "braiins":
                # BraiinsOS miners use REST API for pool configuration
                # Get credentials from config
                all_devices = load_config().get("devices", {})
                device_config = None
                for dev in all_devices.get("braiins", []):
                    if dev.get("ip") == ip:
                        device_config = dev
                        break
                username = device_config.get("username", "root") if device_config else "root"
                bos_password = device_config.get("password", "") if device_config else ""

                # BraiinsOS pool format: stratum+tcp://host:port
                # Normalize pool_url to just hostname
                braiins_host = pool_url.strip()
                for prefix in ['stratum+tcp://', 'stratum+ssl://', 'stratum://', 'tcp://', 'ssl://', 'http://', 'https://']:
                    if braiins_host.lower().startswith(prefix):
                        braiins_host = braiins_host[len(prefix):]
                        break
                braiins_host = braiins_host.rstrip('/')
                if ':' in braiins_host:
                    braiins_host = braiins_host.rsplit(':', 1)[0]
                full_pool_url = f"stratum+tcp://{braiins_host}:{pool_port}"

                # Create pool group with the new pool
                pool_config = {
                    "name": "SpiralPool",
                    "quota": 1,
                    "pools": [{
                        "url": full_pool_url,
                        "user": worker,
                        "password": password
                    }]
                }

                # First try to get existing pools to find the right UID to update
                existing_pools = braiins_api_call(ip, "/pools/", username=username, password=bos_password, timeout=10)
                if not existing_pools.get("error") and existing_pools.get("pool_groups"):
                    # Update first pool group
                    first_group = existing_pools["pool_groups"][0]
                    uid = first_group.get("uid", "")
                    if uid:
                        result = braiins_api_call(ip, f"/pools/{uid}", method="PUT", data=pool_config, username=username, password=bos_password, timeout=10)
                    else:
                        result = braiins_api_call(ip, "/pools/", method="POST", data=pool_config, username=username, password=bos_password, timeout=10)
                else:
                    # Create new pool group
                    result = braiins_api_call(ip, "/pools/", method="POST", data=pool_config, username=username, password=bos_password, timeout=10)

                success = not result.get("error")
                message = result.get("error", "Pool configured via BraiinsOS API")

            elif miner_type in ["avalon", "antminer", "antminer_scrypt", "whatsminer", "innosilicon", "futurebit"]:
                # CGMiner-based miners use socket API
                # Step 1: Get current pools to determine new pool ID
                pools_result = cgminer_command(ip, 4028, "pools")
                current_pool_count = 0
                if "POOLS" in pools_result:
                    current_pool_count = len(pools_result["POOLS"])

                # Step 2: Add the new pool
                full_pool_url = f"{pool_url}:{pool_port}"
                add_param = f"{full_pool_url},{worker},{password}"
                add_result = cgminer_command(ip, 4028, "addpool", add_param)

                if "error" in add_result:
                    results.append({
                        "ip": ip,
                        "name": miner_name,
                        "type": miner_type,
                        "success": False,
                        "message": f"addpool failed: {add_result.get('error')}"
                    })
                    continue

                # Step 3: Switch to the new pool
                new_pool_id = current_pool_count
                switch_result = cgminer_command(ip, 4028, "switchpool", str(new_pool_id))

                success = "error" not in switch_result
                message = f"Pool added and switched to pool {new_pool_id}" if success else f"switchpool failed: {switch_result.get('error')}"

            else:
                # Unknown miner type - try CGMiner API as fallback
                full_pool_url = f"{pool_url}:{pool_port}"
                add_param = f"{full_pool_url},{worker},{password}"
                result = cgminer_command(ip, 4028, "addpool", add_param)
                success = "error" not in result
                message = result.get("error", "Pool added (unknown miner type)")

            results.append({
                "ip": ip,
                "name": miner_name,
                "type": miner_type,
                "success": success,
                "message": message
            })

        except Exception as e:
            results.append({"ip": ip, "name": miner_name, "type": miner_type, "success": False, "message": str(e)})

    success_count = sum(1 for r in results if r["success"])
    return jsonify({
        "success": True,
        "total": len(results),
        "successful": success_count,
        "failed": len(results) - success_count,
        "results": results
    })


@app.route('/api/fleet/maintenance', methods=['GET'])
@api_key_or_login_required
def get_maintenance_mode():
    """ Get maintenance mode status"""
    return jsonify({
        "success": True,
        "maintenance": maintenance_mode
    })


@app.route('/api/fleet/maintenance', methods=['POST'])
@admin_required
def set_maintenance_mode():
    """ Enable/disable maintenance mode"""
    global maintenance_mode
    data = request.json
    enabled = bool(data.get("enabled", False))
    reason = str(data.get("reason", "Scheduled maintenance"))[:256].strip()

    maintenance_mode["enabled"] = enabled
    maintenance_mode["reason"] = reason
    maintenance_mode["paused_alerts"] = bool(data.get("pause_alerts", True))

    if enabled:
        maintenance_mode["started_at"] = datetime.utcnow().isoformat()
    else:
        maintenance_mode["started_at"] = None

    return jsonify({
        "success": True,
        "maintenance": maintenance_mode
    })


@app.route('/api/fleet/status', methods=['GET'])
@api_key_or_login_required
def get_fleet_status():
    """ Get overall fleet status summary"""
    devices = load_config().get("devices", {})

    fleet_stats = {
        "total_miners": 0,
        "online": 0,
        "offline": 0,
        "total_hashrate_ths": 0,
        "avg_temp": 0,
        "by_type": {},
        "by_group": {},
        "maintenance_mode": maintenance_mode["enabled"]
    }

    temps = []

    # Count and categorize miners
    for device_type in ["axeos", "nmaxe", "nerdqaxe", "esp32miner", "qaxe", "qaxeplus", "avalon", "antminer", "antminer_scrypt", "whatsminer", "innosilicon", "goldshell", "hammer", "futurebit", "braiins", "vnish", "luxos", "luckyminer", "jingleminer", "zyber", "gekkoscience", "ipollo", "ebang", "epic", "elphapex", "canaan"]:
        type_count = len(devices.get(device_type, []))
        fleet_stats["total_miners"] += type_count
        fleet_stats["by_type"][device_type] = type_count

    # Count by group
    for group_name, group_data in miner_groups.items():
        fleet_stats["by_group"][group_name] = len(group_data.get("miners", []))

    # Get live status from cache if available
    cached_miners = miner_cache.get("miners", {})
    if cached_miners:
        for miner_name, miner in cached_miners.items():
            if not isinstance(miner, dict):
                continue
            if miner.get("online", False):
                fleet_stats["online"] += 1
                # Use hashrate_ths if available, otherwise convert raw hashrate to TH/s
                # Note: Can't use `or` here because 0 is a valid hashrate
                hashrate_ths = miner.get("hashrate_ths")
                hashrate = hashrate_ths if hashrate_ths is not None else miner.get("hashrate", 0) / 1e12
                fleet_stats["total_hashrate_ths"] += hashrate
                if miner.get("temp"):
                    temps.append(miner["temp"])
            else:
                fleet_stats["offline"] += 1

    if temps:
        fleet_stats["avg_temp"] = sum(temps) / len(temps)

    return jsonify({"success": True, "fleet": fleet_stats})


@app.route('/api/fleet/by-algorithm', methods=['GET'])
@api_key_or_login_required
def get_fleet_by_algorithm():
    """Aggregate online miners by algorithm (sha256d / scrypt)."""
    algos = {}  # {algo_name: {hashrate_ths, hashrate_ghs, workers, power_watts, miners}}

    cached_miners = miner_cache.get("miners", {})
    for miner_name, miner in cached_miners.items():
        if not isinstance(miner, dict) or not miner.get("online"):
            continue
        algo_info = get_miner_algorithm(miner)
        algo_name = algo_info["algorithm"] if algo_info else "unknown"

        if algo_name not in algos:
            algos[algo_name] = {
                "hashrate_ths": 0,
                "hashrate_ghs": 0,
                "workers": 0,
                "power_watts": 0,
                "miners": []
            }

        entry = algos[algo_name]
        hashrate_ths = miner.get("hashrate_ths")
        if hashrate_ths is None:
            hashrate_ths = miner.get("hashrate", 0) / 1e12
        entry["hashrate_ths"] += hashrate_ths
        entry["hashrate_ghs"] += hashrate_ths * 1000
        entry["workers"] += 1
        entry["power_watts"] += miner.get("power", 0) or 0
        entry["miners"].append(miner_name)

    # Round for display
    for algo in algos.values():
        algo["hashrate_ths"] = round(algo["hashrate_ths"], 4)
        algo["hashrate_ghs"] = round(algo["hashrate_ghs"], 2)
        algo["power_watts"] = round(algo["power_watts"], 1)

    return jsonify({"success": True, "algorithms": algos})


# ============================================
#  HISTORICAL ANALYTICS
# ============================================

# NOTE: The primary historical_data dict (deque-based) is defined at the top
# of the file (~line 1374). Do NOT redefine it here — that caused a critical
# bug where the overwrite wiped the deque keys and broke record_historical_data().


def load_historical_data():
    """Load historical data from JSON file into the deque-based historical_data dict."""
    global historical_data
    try:
        history_file = os.path.join(CONFIG_DIR, "historical_data.json")
        if os.path.exists(history_file):
            with open(history_file, "r") as f:
                saved = json.load(f)
            if isinstance(saved, dict):
                for key in historical_data:
                    if key not in saved:
                        continue
                    if isinstance(historical_data[key], dict):
                        # per_miner_hashrate: {name: list} → {name: deque}
                        if not isinstance(saved[key], dict):
                            continue  # Skip corrupt/old-format data
                        historical_data[key].clear()
                        for miner_name, points in saved[key].items():
                            if isinstance(points, list):
                                historical_data[key][miner_name] = deque(points, maxlen=HISTORY_MAX_POINTS)
                    elif isinstance(saved[key], list):
                        historical_data[key].clear()
                        historical_data[key].extend(saved[key])
                loaded_count = sum(
                    sum(len(d) for d in v.values()) if isinstance(v, dict) else len(v)
                    for v in historical_data.values()
                )
                print(f"[HISTORY] Loaded {loaded_count} data points from {history_file}")
    except Exception as e:
        print(f"[HISTORY] Error loading historical data: {e}")


def save_historical_data():
    """Save historical data to JSON file. Converts deques to lists for serialization."""
    try:
        serializable = {}
        for key, val in historical_data.items():
            if isinstance(val, dict):
                # per_miner_hashrate: {name: deque} → {name: list}
                serializable[key] = {k: list(v) for k, v in val.items()}
            else:
                serializable[key] = list(val)
        _atomic_json_save(os.path.join(str(CONFIG_DIR), "historical_data.json"), serializable)
    except Exception as e:
        print(f"[HISTORY] Error saving historical data: {e}")


@app.route('/api/analytics/hashrate', methods=['GET'])
@api_key_or_login_required
def get_hashrate_history():
    """ Get hashrate history for graphing"""
    period = request.args.get("period", "24h")  # 1h, 6h, 24h, 7d, 30d
    resolution = request.args.get("resolution", "auto")  # auto, 1m, 5m, 1h

    # Calculate time range
    now = time.time()
    period_seconds = {
        "1h": 3600,
        "6h": 21600,
        "24h": 86400,
        "7d": 604800,
        "30d": 2592000
    }.get(period, 86400)

    cutoff = now - period_seconds

    # Filter data by time range
    data = [p for p in historical_data.get("hashrate", []) if p.get("timestamp", 0) > cutoff]

    # Downsample if needed
    if resolution == "auto":
        target_points = 200
        if len(data) > target_points:
            step = len(data) // target_points
            data = data[::step]

    return jsonify({
        "success": True,
        "period": period,
        "points": len(data),
        "data": data
    })


@app.route('/api/analytics/shares', methods=['GET'])
@api_key_or_login_required
def get_share_history():
    """ Get share acceptance rate history"""
    period = request.args.get("period", "24h")

    now = time.time()
    period_seconds = {
        "1h": 3600,
        "6h": 21600,
        "24h": 86400,
        "7d": 604800,
        "30d": 2592000
    }.get(period, 86400)

    cutoff = now - period_seconds
    data = [p for p in historical_data.get("shares", []) if p.get("timestamp", 0) > cutoff]

    # Calculate acceptance rate over time
    acceptance_rates = []
    for point in data:
        accepted = point.get("accepted", 0)
        rejected = point.get("rejected", 0)
        total = accepted + rejected
        rate = (accepted / total * 100) if total > 0 else 100
        acceptance_rates.append({
            "timestamp": point["timestamp"],
            "acceptance_rate": round(rate, 2),
            "accepted": accepted,
            "rejected": rejected
        })

    # Calculate overall stats
    total_accepted = sum(p.get("accepted", 0) for p in data)
    total_rejected = sum(p.get("rejected", 0) for p in data)
    overall_rate = (total_accepted / (total_accepted + total_rejected) * 100) if (total_accepted + total_rejected) > 0 else 100

    return jsonify({
        "success": True,
        "period": period,
        "data": acceptance_rates,
        "summary": {
            "total_accepted": total_accepted,
            "total_rejected": total_rejected,
            "overall_acceptance_rate": round(overall_rate, 2)
        }
    })


@app.route('/api/analytics/temperature', methods=['GET'])
@api_key_or_login_required
def get_temperature_history():
    """ Get temperature history across fleet"""
    period = request.args.get("period", "24h")

    now = time.time()
    period_seconds = {"1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000}.get(period, 86400)
    cutoff = now - period_seconds

    data = [p for p in historical_data.get("temperature", []) if p.get("timestamp", 0) > cutoff]

    return jsonify({
        "success": True,
        "period": period,
        "data": data,
        "summary": {
            "current_avg": data[-1].get("avg") if data else 0,
            "period_max": max(p.get("max", 0) for p in data) if data else 0,
            "period_min": min(p.get("min", 100) for p in data) if data else 0
        }
    })


@app.route('/api/analytics/efficiency', methods=['GET'])
@api_key_or_login_required
def get_efficiency_history():
    """ Get efficiency (J/TH) history"""
    period = request.args.get("period", "24h")

    now = time.time()
    period_seconds = {"1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000}.get(period, 86400)
    cutoff = now - period_seconds

    data = [p for p in historical_data.get("efficiency", []) if p.get("timestamp", 0) > cutoff]

    return jsonify({
        "success": True,
        "period": period,
        "data": data,
        "summary": {
            "current": data[-1].get("value") if data else 0,
            "period_avg": sum(p.get("value", 0) for p in data) / len(data) if data else 0
        }
    })


# ============================================
#  NETWORK HEALTH MONITOR
# ============================================

network_health_cache = {
    "difficulty_history": [],
    "hashrate_history": [],
    "block_times": [],
    "last_update": 0
}


def fetch_network_stats():
    """Fetch current network statistics from daemon/pool.

    Tries the dedicated /network endpoint first (V2 API), then falls back
    to pool_stats_cache which is already populated from /api/pools polling.
    Normalizes key names so callers always get: networkDifficulty, networkHashrate.
    """
    # Try dedicated network endpoint (V2 API)
    try:
        resp = _http_session.get(f"{POOL_API_URL}/api/pools/{get_pool_id()}/network", timeout=5)
        if resp.status_code == 200:
            data = resp.json()
            # V2 API returns "difficulty"/"hashrate", normalize to expected keys
            return {
                "networkDifficulty": data.get("networkDifficulty") or data.get("difficulty", 0),
                "networkHashrate": data.get("networkHashrate") or data.get("hashrate", 0),
                "blockHeight": data.get("blockHeight") or data.get("height", 0),
            }
    except (requests.exceptions.RequestException, ValueError, KeyError):
        pass

    # Fallback: use already-cached pool stats (populated by fetch_pool_stats every 10s)
    with _pool_stats_lock:
        diff = pool_stats_cache.get("network_difficulty", 0)
        height = pool_stats_cache.get("block_height", 0)
    if diff:
        return {
            "networkDifficulty": diff,
            "networkHashrate": 0,
            "blockHeight": height,
        }

    return {}


def update_network_health():
    """Update network health cache"""
    global network_health_cache

    now = time.time()
    if now - network_health_cache["last_update"] < 60:  # Update every 60 seconds
        return

    net_stats = fetch_network_stats()
    if not net_stats:
        return

    # Record difficulty
    network_health_cache["difficulty_history"].append({
        "timestamp": now,
        "difficulty": net_stats.get("networkDifficulty", 0)
    })

    # Record network hashrate
    network_health_cache["hashrate_history"].append({
        "timestamp": now,
        "hashrate": net_stats.get("networkHashrate", 0)
    })

    # Record block time if available
    if "lastBlockTime" in net_stats:
        network_health_cache["block_times"].append({
            "timestamp": now,
            "block_time": net_stats.get("lastBlockTime", 0)
        })

    # Trim old data (keep 7 days at 1-minute intervals)
    max_points = 10080
    for key in ["difficulty_history", "hashrate_history", "block_times"]:
        if len(network_health_cache[key]) > max_points:
            network_health_cache[key] = network_health_cache[key][-max_points:]

    network_health_cache["last_update"] = now


@app.route('/api/network/health', methods=['GET'])
@api_key_or_login_required
def get_network_health():
    """ Get current network health status"""
    net_stats = fetch_network_stats()

    return jsonify({
        "success": True,
        "network": {
            "difficulty": net_stats.get("networkDifficulty", 0),
            "hashrate": net_stats.get("networkHashrate", 0),
            "hashrate_ths": net_stats.get("networkHashrate", 0) / 1e12,
            "hashrate_phs": net_stats.get("networkHashrate", 0) / 1e15,
            "block_height": net_stats.get("blockHeight", 0),
            "last_block_time": net_stats.get("lastBlockTime", 0),
            "block_reward": net_stats.get("blockReward", COIN_BLOCK_REWARDS.get(get_enabled_coins().get("primary", "BTC"), 0)),
            "coin": get_enabled_coins().get("primary", "Unknown"),
            "algorithm": get_algorithm_for_coin(get_enabled_coins().get("primary", "BTC"))
        }
    })


@app.route('/api/network/difficulty', methods=['GET'])
@api_key_or_login_required
def get_difficulty_history():
    """ Get difficulty trend over time"""
    period = request.args.get("period", "24h")

    now = time.time()
    period_seconds = {"1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000}.get(period, 86400)
    cutoff = now - period_seconds

    data = [p for p in network_health_cache.get("difficulty_history", []) if p.get("timestamp", 0) > cutoff]

    # Calculate trend
    if len(data) >= 2:
        first_diff = data[0].get("difficulty", 0)
        last_diff = data[-1].get("difficulty", 0)
        change_pct = ((last_diff - first_diff) / first_diff * 100) if first_diff > 0 else 0
    else:
        change_pct = 0

    return jsonify({
        "success": True,
        "period": period,
        "data": data,
        "summary": {
            "current": data[-1].get("difficulty", 0) if data else 0,
            "period_high": max(p.get("difficulty", 0) for p in data) if data else 0,
            "period_low": min(p.get("difficulty", 0) for p in data) if data else 0,
            "change_percent": round(change_pct, 2)
        }
    })


@app.route('/api/network/hashrate', methods=['GET'])
@api_key_or_login_required
def get_network_hashrate_history():
    """ Get network hashrate trend"""
    period = request.args.get("period", "24h")

    now = time.time()
    period_seconds = {"1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000}.get(period, 86400)
    cutoff = now - period_seconds

    data = [p for p in network_health_cache.get("hashrate_history", []) if p.get("timestamp", 0) > cutoff]

    # Convert to TH/s for display
    display_data = [{
        "timestamp": p["timestamp"],
        "hashrate_ths": p.get("hashrate", 0) / 1e12
    } for p in data]

    return jsonify({
        "success": True,
        "period": period,
        "data": display_data,
        "summary": {
            "current_ths": display_data[-1].get("hashrate_ths", 0) if display_data else 0,
            "period_avg_ths": sum(p.get("hashrate_ths", 0) for p in display_data) / len(display_data) if display_data else 0
        }
    })


@app.route('/api/network/blocktimes', methods=['GET'])
@api_key_or_login_required
def get_block_time_analysis():
    """ Get block time analysis.

    Multi-coin support:
    - Optional ?coin=BTC parameter to get block times for specific coin
    - Defaults to primary coin
    - Uses COIN_BLOCK_TIMES for target block time per coin

    OWASP: A03 - Coin validated against whitelist
    """
    period = request.args.get("period", "24h")

    # Get coin parameter (optional)
    coin_param = request.args.get('coin', '').upper()
    primary_coin = get_primary_coin()

    # SECURITY: Validate coin parameter against whitelist
    if coin_param:
        if coin_param not in MULTI_COIN_NODES:
            return jsonify({"success": False, "error": f"Invalid coin: {coin_param}"}), 400
        coin = coin_param
    else:
        coin = primary_coin

    # Get target block time for this coin
    target_block_time = COIN_BLOCK_TIMES.get(coin, 15)

    now = time.time()
    period_seconds = {"1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800}.get(period, 86400)
    cutoff = now - period_seconds

    data = [p for p in network_health_cache.get("block_times", []) if p.get("timestamp", 0) > cutoff]

    if data:
        block_times = [p.get("block_time", target_block_time) for p in data]
        avg_time = sum(block_times) / len(block_times)
        min_time = min(block_times)
        max_time = max(block_times)
    else:
        avg_time = min_time = max_time = target_block_time

    return jsonify({
        "success": True,
        "period": period,
        "coin": coin,
        "data": data,
        "summary": {
            "target_block_time": target_block_time,
            "average_block_time": round(avg_time, 2),
            "min_block_time": min_time,
            "max_block_time": max_time,
            "blocks_analyzed": len(data),
            "variance_from_target": round(((avg_time - target_block_time) / target_block_time) * 100, 1) if avg_time else 0
        }
    })


@app.route('/api/network/pools', methods=['GET'])
@api_key_or_login_required
def get_network_pools():
    """ Get known mining pools on the network (informational).

    Multi-coin support:
    - Optional ?coin=BTC parameter to get pools for specific coin
    - Defaults to primary coin

    OWASP: A03 - Coin validated against whitelist
    """
    # Get coin parameter (optional)
    coin_param = request.args.get('coin', '').upper()
    primary_coin = get_primary_coin()

    # SECURITY: Validate coin parameter against whitelist
    if coin_param:
        if coin_param not in MULTI_COIN_NODES:
            return jsonify({"success": False, "error": f"Invalid coin: {coin_param}"}), 400
        coin = coin_param
    else:
        coin = primary_coin

    # MINING COSTS DISCLAIMER:
    # This software does not charge pool fees (SOLO mining architecture).
    # Users are SOLELY responsible for ALL costs associated with mining, including but
    # not limited to: electricity costs, hardware costs, network/transaction fees,
    # hashrate rental fees (if using third-party services), cooling costs, maintenance,
    # and any other operational expenses. The authors make no representations about
    # mining profitability and accept no liability for any costs incurred by users.
    # Mining cryptocurrency involves significant financial risk.

    return jsonify({
        "success": True,
        "coin": coin,
        "pool_type": "solo",
        "disclaimer": "SOLO mining - no pool fees. User is solely responsible for all mining costs including electricity, hardware, transaction fees, and any third-party service fees."
    })


# ============================================
# V1.0: SHARE SUBMISSION HEATMAP
# ============================================

def record_share_to_heatmap():
    """Record a share submission to the heatmap"""
    global share_heatmap

    now = datetime.utcnow()
    day_of_week = now.weekday()  # 0=Monday, 6=Sunday (UTC)
    hour = now.hour  # UTC hour

    with _share_heatmap_lock:
        # Reset if more than a week old
        if time.time() - share_heatmap["last_reset"] > 604800:  # 7 days
            share_heatmap["data"] = [[0 for _ in range(24)] for _ in range(7)]
            share_heatmap["last_reset"] = time.time()

        share_heatmap["data"][day_of_week][hour] += 1


@app.route('/api/analytics/heatmap', methods=['GET'])
@api_key_or_login_required
def get_share_heatmap():
    """V1.0: Get share submission heatmap data"""
    days = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"]

    with _share_heatmap_lock:
        # Take a snapshot of the data under lock
        heatmap_snapshot = [row[:] for row in share_heatmap["data"]]
        last_reset = share_heatmap["last_reset"]

    heatmap_data = []
    for day_idx, day_name in enumerate(days):
        for hour in range(24):
            heatmap_data.append({
                "day": day_name,
                "day_idx": day_idx,
                "hour": hour,
                "hour_label": f"{hour:02d}:00",
                "shares": heatmap_snapshot[day_idx][hour]
            })

    # Find peak times (handle empty heatmap - all zeros)
    row_maxes = [max(row) if row else 0 for row in heatmap_snapshot]
    max_shares = max(row_maxes) if row_maxes else 0
    peak_times = []
    for day_idx, day_data in enumerate(heatmap_snapshot):
        for hour, count in enumerate(day_data):
            if count == max_shares and max_shares > 0:
                peak_times.append({"day": days[day_idx], "hour": f"{hour:02d}:00"})

    return jsonify({
        "success": True,
        "heatmap": heatmap_data,
        "summary": {
            "peak_times": peak_times,
            "total_shares": sum(sum(row) for row in heatmap_snapshot),
            "max_hourly": max_shares,
            "days_tracked": min(7, int((time.time() - last_reset) / 86400) + 1)
        }
    })


# ============================================
# V1.0: FIRMWARE VERSION TRACKER
# ============================================

def update_firmware_info(ip, version, device_type):
    """Update firmware info for a miner"""
    global firmware_tracker

    known = firmware_tracker["known_versions"]
    update_available = False

    # Check if update is available based on device type (AxeOS-based devices)
    if device_type in ["bitaxe", "nmaxe", "axeos", "nerdqaxe", "hammer"]:
        known_ver = known.get(device_type, "0.0.0")
        try:
            # Simple version comparison
            update_available = version < known_ver
        except (ValueError, TypeError):
            pass

    firmware_tracker["miners"][ip] = {
        "version": version,
        "device_type": device_type,
        "last_seen": time.time(),
        "update_available": update_available
    }
    firmware_tracker["last_update"] = time.time()


@app.route('/api/firmware/status', methods=['GET'])
@api_key_or_login_required
def get_firmware_status():
    """V1.0: Get firmware version status for all miners"""
    now = time.time()

    # Filter to miners seen in last hour
    active_miners = {
        ip: info for ip, info in firmware_tracker["miners"].items()
        if now - info.get("last_seen", 0) < 3600
    }

    # Count by status
    up_to_date = sum(1 for m in active_miners.values() if not m.get("update_available", False))
    needs_update = sum(1 for m in active_miners.values() if m.get("update_available", False))

    # Group by device type
    by_type = {}
    for ip, info in active_miners.items():
        dtype = info.get("device_type", "unknown")
        if dtype not in by_type:
            by_type[dtype] = []
        by_type[dtype].append({
            "ip": ip,
            "version": info.get("version", "unknown"),
            "update_available": info.get("update_available", False),
            "latest_version": firmware_tracker["known_versions"].get(dtype, "unknown")
        })

    return jsonify({
        "success": True,
        "miners": active_miners,
        "by_device_type": by_type,
        "summary": {
            "total_miners": len(active_miners),
            "up_to_date": up_to_date,
            "needs_update": needs_update,
            "known_latest_versions": firmware_tracker["known_versions"]
        }
    })


@app.route('/api/firmware/known-versions', methods=['POST'])
@admin_required
def update_known_versions():
    """V1.0: Update known latest firmware versions"""
    data = request.get_json() or {}

    for device_type, version in data.items():
        if isinstance(version, str) and device_type:
            firmware_tracker["known_versions"][device_type] = version

    return jsonify({"success": True, "versions": firmware_tracker["known_versions"]})


# ============================================
# V1.0: DOWNTIME TRACKING
# ============================================

def record_miner_status(ip, is_online, miner_name=None):
    """Record miner online/offline status for downtime tracking"""
    global downtime_tracker

    now = time.time()

    if ip not in downtime_tracker["miners"]:
        downtime_tracker["miners"][ip] = {
            "name": miner_name or ip,
            "total_downtime_sec": 0,
            "downtime_events": [],
            "last_online": now if is_online else None,
            "went_offline_at": None if is_online else now,
            "current_status": "online" if is_online else "offline"
        }
        return

    miner = downtime_tracker["miners"][ip]
    previous_status = miner["current_status"]

    if is_online and previous_status == "offline":
        # Coming back online - record downtime event
        if miner.get("went_offline_at"):
            duration = now - miner["went_offline_at"]
            event = {
                "miner_ip": ip,
                "miner_name": miner_name or ip,
                "start_time": miner["went_offline_at"],
                "end_time": now,
                "duration_sec": duration
            }
            miner["downtime_events"].append(event)
            downtime_tracker["events"].append(event)
            miner["total_downtime_sec"] += duration

            # Keep only last 100 events per miner
            miner["downtime_events"] = miner["downtime_events"][-100:]

        miner["last_online"] = now
        miner["went_offline_at"] = None
        miner["current_status"] = "online"
        display_name = miner_name or ip
        record_activity("miner_online", f"Miner online: {display_name}", {"ip": ip})

    elif not is_online and previous_status == "online":
        # Going offline
        miner["went_offline_at"] = now
        miner["current_status"] = "offline"
        display_name = miner_name or ip
        record_activity("miner_offline", f"Miner offline: {display_name}", {"ip": ip})

    elif is_online:
        miner["last_online"] = now

    # Keep only last 1000 global events
    downtime_tracker["events"] = downtime_tracker["events"][-1000:]
    downtime_tracker["last_update"] = now


@app.route('/api/downtime/status', methods=['GET'])
@api_key_or_login_required
def get_downtime_status():
    """V1.0: Get downtime tracking status"""
    now = time.time()

    # Calculate current downtime for offline miners
    miners_with_current = {}
    for ip, miner in downtime_tracker["miners"].items():
        miner_copy = dict(miner)
        if miner["current_status"] == "offline" and miner.get("went_offline_at"):
            miner_copy["current_downtime_sec"] = now - miner["went_offline_at"]
        else:
            miner_copy["current_downtime_sec"] = 0
        miners_with_current[ip] = miner_copy

    # Recent events (last 24h)
    cutoff = now - 86400
    recent_events = [e for e in downtime_tracker["events"] if e.get("start_time", 0) > cutoff]

    # Calculate uptime percentages
    uptime_data = {}
    for ip, miner in miners_with_current.items():
        total_tracked = now - (miner.get("first_seen", now) or now)
        if total_tracked > 0:
            total_down = miner["total_downtime_sec"] + miner.get("current_downtime_sec", 0)
            uptime_pct = max(0, (1 - total_down / total_tracked) * 100)
        else:
            uptime_pct = 100
        uptime_data[ip] = round(uptime_pct, 2)

    return jsonify({
        "success": True,
        "miners": miners_with_current,
        "uptime_percentages": uptime_data,
        "recent_events": recent_events[-50:],  # Last 50 events
        "summary": {
            "total_miners": len(miners_with_current),
            "online": sum(1 for m in miners_with_current.values() if m["current_status"] == "online"),
            "offline": sum(1 for m in miners_with_current.values() if m["current_status"] == "offline"),
            "events_24h": len(recent_events)
        }
    })


# ============================================
# ACTIVITY FEED
# ============================================

def record_activity(event_type, message, details=None):
    """Append an event to the activity feed.

    Args:
        event_type: One of 'block', 'miner_offline', 'miner_online', 'alert', 'config', 'restart'
        message: Human-readable description
        details: Optional dict with extra context
    """
    activity_feed["events"].append({
        "timestamp": time.time(),
        "type": event_type,
        "message": message,
        "details": details or {}
    })


def save_activity_feed():
    """Persist activity feed to disk."""
    try:
        data = [dict(e) for e in activity_feed["events"]]
        _atomic_json_save(os.path.join(str(CONFIG_DIR), "activity_feed.json"), data)
        activity_feed["last_save"] = time.time()
    except Exception as e:
        print(f"[ACTIVITY] Error saving activity feed: {e}")


def load_activity_feed():
    """Load activity feed from disk."""
    try:
        feed_file = os.path.join(str(CONFIG_DIR), "activity_feed.json")
        if os.path.exists(feed_file):
            with open(feed_file, "r") as f:
                saved = json.load(f)
            if isinstance(saved, list):
                activity_feed["events"].clear()
                for event in saved:
                    if isinstance(event, dict):
                        activity_feed["events"].append(event)
                print(f"[ACTIVITY] Loaded {len(activity_feed['events'])} events from {feed_file}")
    except Exception as e:
        print(f"[ACTIVITY] Error loading activity feed: {e}")


@app.route('/api/activity', methods=['GET'])
@api_key_or_login_required
def get_activity_feed():
    """Return recent activity feed events."""
    try:
        limit = min(int(request.args.get('limit', 50)), 200)
    except (ValueError, TypeError):
        limit = 50
    events = list(activity_feed["events"])
    # Return most recent first
    events.reverse()
    return jsonify({"success": True, "events": events[:limit]})


# ============================================
# V1.0: PERFORMANCE DEGRADATION TRACKING
# ============================================

def record_hashrate_sample(ip, hashrate_ths, miner_name=None):
    """Record hashrate sample for degradation tracking"""
    global performance_tracker

    now = time.time()

    if ip not in performance_tracker["miners"]:
        performance_tracker["miners"][ip] = {
            "name": miner_name or ip,
            "hashrate_baseline": hashrate_ths,
            "hashrate_samples": [],
            "degradation_percent": 0,
            "last_alert": None
        }

    miner = performance_tracker["miners"][ip]

    # Add sample
    miner["hashrate_samples"].append({
        "timestamp": now,
        "hashrate_ths": hashrate_ths
    })

    # Keep only samples from last 24 hours
    cutoff = now - (performance_tracker["sample_window_hours"] * 3600)
    miner["hashrate_samples"] = [s for s in miner["hashrate_samples"] if s["timestamp"] > cutoff]

    # Update baseline (rolling 24h average)
    if len(miner["hashrate_samples"]) > 10:
        avg_hashrate = sum(s["hashrate_ths"] for s in miner["hashrate_samples"]) / len(miner["hashrate_samples"])

        # Update baseline if current average is higher
        if avg_hashrate > miner["hashrate_baseline"]:
            miner["hashrate_baseline"] = avg_hashrate

        # Calculate degradation from baseline
        if miner["hashrate_baseline"] > 0:
            current = hashrate_ths
            baseline = miner["hashrate_baseline"]
            degradation = ((baseline - current) / baseline) * 100
            miner["degradation_percent"] = round(max(0, degradation), 2)

    performance_tracker["last_update"] = now


@app.route('/api/performance/degradation', methods=['GET'])
@api_key_or_login_required
def get_performance_degradation():
    """V1.0: Get performance degradation status"""
    threshold = performance_tracker["degradation_threshold"]

    degraded_miners = []
    healthy_miners = []

    for ip, miner in performance_tracker["miners"].items():
        miner_info = {
            "ip": ip,
            "name": miner.get("name", ip),
            "baseline_ths": round(miner.get("hashrate_baseline", 0), 2),
            "current_ths": round(miner["hashrate_samples"][-1]["hashrate_ths"], 2) if miner["hashrate_samples"] else 0,
            "degradation_percent": miner.get("degradation_percent", 0),
            "samples_count": len(miner.get("hashrate_samples", []))
        }

        if miner.get("degradation_percent", 0) >= threshold:
            degraded_miners.append(miner_info)
        else:
            healthy_miners.append(miner_info)

    return jsonify({
        "success": True,
        "degraded": degraded_miners,
        "healthy": healthy_miners,
        "threshold_percent": threshold,
        "summary": {
            "total_miners": len(performance_tracker["miners"]),
            "degraded_count": len(degraded_miners),
            "healthy_count": len(healthy_miners)
        }
    })


# ============================================
# V1.0: SHARE REJECTION ANALYSIS
# ============================================

def record_share_result(miner_ip, accepted, rejection_reason=None):
    """Record share result for rejection analysis"""
    global rejection_analysis

    now = time.time()

    if accepted:
        rejection_analysis["total_accepted"] += 1
    else:
        rejection_analysis["total_rejected"] += 1
        if rejection_reason:
            reason = str(rejection_reason)
            rejection_analysis["rejection_reasons"][reason] = \
                rejection_analysis["rejection_reasons"].get(reason, 0) + 1

    # Track by miner
    if miner_ip not in rejection_analysis["by_miner"]:
        rejection_analysis["by_miner"][miner_ip] = {
            "accepted": 0,
            "rejected": 0,
            "rejection_rate": 0
        }

    miner = rejection_analysis["by_miner"][miner_ip]
    if accepted:
        miner["accepted"] += 1
    else:
        miner["rejected"] += 1

    total = miner["accepted"] + miner["rejected"]
    miner["rejection_rate"] = round((miner["rejected"] / total * 100) if total > 0 else 0, 2)

    # Record hourly rate
    current_hour = int(now / 3600) * 3600
    if rejection_analysis["hourly_rejection_rate"]:
        last = rejection_analysis["hourly_rejection_rate"][-1]
        if last.get("timestamp") == current_hour:
            # Update current hour
            last["accepted" if accepted else "rejected"] = last.get("accepted" if accepted else "rejected", 0) + 1
            total_this_hour = last.get("accepted", 0) + last.get("rejected", 0)
            last["rate"] = round((last.get("rejected", 0) / total_this_hour * 100) if total_this_hour > 0 else 0, 2)
        else:
            # New hour
            rejection_analysis["hourly_rejection_rate"].append({
                "timestamp": current_hour,
                "accepted": 1 if accepted else 0,
                "rejected": 0 if accepted else 1,
                "rate": 0 if accepted else 100
            })
    else:
        rejection_analysis["hourly_rejection_rate"].append({
            "timestamp": current_hour,
            "accepted": 1 if accepted else 0,
            "rejected": 0 if accepted else 1,
            "rate": 0 if accepted else 100
        })

    # Keep last 168 hours (7 days)
    rejection_analysis["hourly_rejection_rate"] = rejection_analysis["hourly_rejection_rate"][-168:]
    rejection_analysis["last_update"] = now


def update_rejection_analysis():
    """Populate rejection_analysis from prometheus metrics and miner cache.

    The stratum exports labeled counters:
      stratum_shares_accepted_total
      stratum_shares_rejected_total{reason="stale"}
      stratum_shares_rejected_total{reason="low_difficulty"}
      stratum_shares_rejected_total{reason="duplicate"}
      stratum_shares_stale_total

    This function reads those counters (already cached by fetch_prometheus_metrics)
    and updates rejection_analysis so the /api/shares/rejection-analysis endpoint
    returns real data.
    """
    global rejection_analysis

    metrics = prometheus_cache.get("metrics", {})
    if not metrics:
        return

    now = time.time()

    # Totals from prometheus counters
    accepted = int(metrics.get("stratum_shares_accepted_total", 0))
    rejected = int(metrics.get("stratum_shares_rejected_total", 0))

    if accepted == 0 and rejected == 0:
        return

    rejection_analysis["total_accepted"] = accepted
    rejection_analysis["total_rejected"] = rejected

    # Extract per-reason breakdown from labeled prometheus keys
    # Keys look like: stratum_shares_rejected_total{reason="stale"}
    reasons = {}
    for key, value in metrics.items():
        if key.startswith('stratum_shares_rejected_total{') and 'reason=' in key:
            # Extract reason from label: reason="stale" -> stale
            try:
                reason = key.split('reason="')[1].split('"')[0]
                reasons[reason] = int(value)
            except (IndexError, ValueError):
                pass
    if reasons:
        rejection_analysis["rejection_reasons"] = reasons

    # Per-miner rejection rates from miner_cache (already polled)
    with _miner_cache_lock:
        miners = miner_cache.get("miners", {})

    by_miner = {}
    for name, data in miners.items():
        if not isinstance(data, dict) or not data.get("online"):
            continue
        acc = data.get("accepted", 0)
        rej = data.get("rejected", 0)
        if acc > 0 or rej > 0:
            total = acc + rej
            by_miner[data.get("ip", name)] = {
                "accepted": acc,
                "rejected": rej,
                "rejection_rate": round((rej / total * 100) if total > 0 else 0, 2)
            }
    if by_miner:
        rejection_analysis["by_miner"] = by_miner

    # Hourly trend snapshot
    current_hour = int(now / 3600) * 3600
    hourly = rejection_analysis["hourly_rejection_rate"]
    total_shares = accepted + rejected
    current_rate = round((rejected / total_shares * 100) if total_shares > 0 else 0, 2)

    if hourly and hourly[-1].get("timestamp") == current_hour:
        # Update current hour bucket
        hourly[-1]["accepted"] = accepted
        hourly[-1]["rejected"] = rejected
        hourly[-1]["rate"] = current_rate
    else:
        # New hour bucket
        hourly.append({
            "timestamp": current_hour,
            "accepted": accepted,
            "rejected": rejected,
            "rate": current_rate
        })

    rejection_analysis["hourly_rejection_rate"] = hourly[-168:]
    rejection_analysis["last_update"] = now


@app.route('/api/shares/rejection-analysis', methods=['GET'])
@api_key_or_login_required
def get_rejection_analysis():
    """V1.0: Get share rejection analysis"""
    total = rejection_analysis["total_accepted"] + rejection_analysis["total_rejected"]
    overall_rate = round((rejection_analysis["total_rejected"] / total * 100) if total > 0 else 0, 2)

    # Sort reasons by count
    sorted_reasons = sorted(
        rejection_analysis["rejection_reasons"].items(),
        key=lambda x: x[1],
        reverse=True
    )

    # Top problematic miners
    miners_by_rejection = sorted(
        rejection_analysis["by_miner"].items(),
        key=lambda x: x[1].get("rejection_rate", 0),
        reverse=True
    )

    return jsonify({
        "success": True,
        "overall": {
            "total_accepted": rejection_analysis["total_accepted"],
            "total_rejected": rejection_analysis["total_rejected"],
            "rejection_rate": overall_rate
        },
        "rejection_reasons": [{"reason": r, "count": c} for r, c in sorted_reasons[:10]],
        "by_miner": {ip: data for ip, data in miners_by_rejection[:20]},
        "hourly_trend": rejection_analysis["hourly_rejection_rate"][-24:]  # Last 24 hours
    })


# ============================================
# V1.0: UPTIME REPORTS
# ============================================

@app.route('/api/uptime/report', methods=['GET'])
@api_key_or_login_required
def get_uptime_report():
    """V1.0: Get comprehensive uptime report"""
    period = request.args.get("period", "24h")

    now = time.time()
    period_seconds = {"1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000}.get(period, 86400)
    cutoff = now - period_seconds

    # Calculate per-miner uptime
    miner_uptime = {}
    for ip, miner in downtime_tracker["miners"].items():
        # Calculate downtime in period
        period_downtime = 0
        for event in miner.get("downtime_events", []):
            event_start = event.get("start_time", 0)
            event_end = event.get("end_time", now)

            if event_end > cutoff:
                overlap_start = max(event_start, cutoff)
                overlap_end = event_end
                period_downtime += (overlap_end - overlap_start)

        # Add current downtime if offline
        if miner["current_status"] == "offline" and miner.get("went_offline_at"):
            offline_start = max(miner["went_offline_at"], cutoff)
            period_downtime += (now - offline_start)

        uptime_pct = max(0, (1 - period_downtime / period_seconds) * 100)

        miner_uptime[ip] = {
            "name": miner.get("name", ip),
            "uptime_percent": round(uptime_pct, 2),
            "downtime_seconds": round(period_downtime, 0),
            "current_status": miner["current_status"]
        }

    # Farm totals
    total_uptime_pct = sum(m["uptime_percent"] for m in miner_uptime.values()) / len(miner_uptime) if miner_uptime else 100
    total_downtime = sum(m["downtime_seconds"] for m in miner_uptime.values())

    # Categorize miners
    excellent = [ip for ip, m in miner_uptime.items() if m["uptime_percent"] >= 99]
    good = [ip for ip, m in miner_uptime.items() if 95 <= m["uptime_percent"] < 99]
    poor = [ip for ip, m in miner_uptime.items() if m["uptime_percent"] < 95]

    return jsonify({
        "success": True,
        "period": period,
        "miners": miner_uptime,
        "farm": {
            "average_uptime_percent": round(total_uptime_pct, 2),
            "total_downtime_seconds": total_downtime,
            "total_miners": len(miner_uptime)
        },
        "categories": {
            "excellent_99plus": len(excellent),
            "good_95_99": len(good),
            "poor_below_95": len(poor)
        },
        "problematic_miners": [miner_uptime[ip] for ip in poor[:5]]  # Top 5 worst
    })


# ============================================
# V1.0: POWER COST & PROFITABILITY
# ============================================

def update_power_cost_tracking():
    """Update power cost and profitability calculations"""
    global power_cost_tracker

    config = load_config()
    power_config = config.get("power_cost", {})

    rate_per_kwh = power_config.get("rate_per_kwh", 0.12)

    # Get current power consumption from miner cache
    total_watts = miner_cache.get("totals", {}).get("power_watts", 0)

    # Calculate daily usage
    hours_per_day = 24
    daily_kwh = (total_watts / 1000) * hours_per_day
    daily_cost = daily_kwh * rate_per_kwh

    # Monthly projections
    monthly_kwh = daily_kwh * 30
    monthly_cost = monthly_kwh * rate_per_kwh

    power_cost_tracker["daily_kwh"] = round(daily_kwh, 2)
    power_cost_tracker["daily_cost"] = round(daily_cost, 2)
    power_cost_tracker["monthly_kwh"] = round(monthly_kwh, 2)
    power_cost_tracker["monthly_cost"] = round(monthly_cost, 2)
    power_cost_tracker["last_update"] = time.time()


@app.route('/api/currency', methods=['GET'])
def currency_info_endpoint():
    """V1.0: Get supported currencies and current configuration."""
    config = load_config()
    power_config = config.get("power_cost", {})
    power_currency = power_config.get("currency", "CAD").upper()

    # Display currency preference (single currency mode)
    # Support both old (display_currency_primary) and new (display_currency) config keys
    display_code = config.get("display_currency",
                     config.get("display_currency_primary", "CAD")).upper()
    display_meta = DASHBOARD_CURRENCIES.get(display_code, DASHBOARD_CURRENCIES["CAD"])

    # Build prices dict from cache
    prices = {}
    for cur_code in DASHBOARD_VS_CURRENCIES.split(","):
        prices[cur_code] = block_reward_cache.get(f"price_{cur_code}", 0)

    return jsonify({
        "supported": DASHBOARD_CURRENCIES,
        "currency": {"code": display_code, **display_meta},
        "power_currency": power_currency,
        "power_currency_meta": DASHBOARD_CURRENCIES.get(power_currency, DASHBOARD_CURRENCIES["CAD"]),
        "prices": prices,
        "coin": block_reward_cache.get("coin"),
    })


@app.route('/api/power/config', methods=['GET', 'POST'])
@api_key_or_login_required
def power_config_endpoint():
    """V1.0: Get or set power cost configuration"""
    config = load_config()

    if request.method == 'POST':
        data = request.get_json() or {}

        if "power_cost" not in config:
            config["power_cost"] = {}

        if "currency" in data:
            config["power_cost"]["currency"] = data["currency"]
        if "currency_symbol" in data:
            config["power_cost"]["currency_symbol"] = data["currency_symbol"]
        if "rate_per_kwh" in data:
            try:
                rate = float(data["rate_per_kwh"])
            except (ValueError, TypeError):
                return jsonify({"success": False, "error": "rate_per_kwh must be a number"}), 400
            if rate > 100:
                return jsonify({"success": False, "error": "rate_per_kwh exceeds maximum (100)"}), 400
            config["power_cost"]["rate_per_kwh"] = rate

            # Easter egg for free power! (triggers on 0, 0.0, 0.00, etc.)
            if rate <= 0:
                config["power_cost"]["is_free_power"] = True
                return jsonify({
                    "success": True,
                    "message": "Configuration saved!",
                    "easter_egg": True,
                    "celebration": """
Power cost configured as $0.00/kWh.

NOTE: This setting affects display calculations only.
Actual mining outcomes depend on many factors outside
this software's control including network difficulty,
hardware performance, electricity rates, and market prices.

No guarantees of profitability are made or implied.
See WARNINGS.md for important risk disclosures.
""",
                    "config": config["power_cost"]
                })
            else:
                config["power_cost"]["is_free_power"] = False

        save_config(config)
        update_power_cost_tracking()

        return jsonify({"success": True, "config": config.get("power_cost", {})})

    return jsonify({
        "success": True,
        "config": config.get("power_cost", DEFAULT_CONFIG["power_cost"])
    })


@app.route('/api/power/stats', methods=['GET'])
@api_key_or_login_required
def get_power_stats():
    """V1.0: Get power consumption and cost statistics"""
    update_power_cost_tracking()

    config = load_config()
    power_config = config.get("power_cost", {})

    # Get current power consumption
    total_watts = miner_cache.get("totals", {}).get("power_watts", 0)
    total_hashrate_ths = miner_cache.get("totals", {}).get("hashrate_ths", 0)

    # Calculate efficiency (J/TH)
    efficiency_jth = (total_watts / total_hashrate_ths) if total_hashrate_ths > 0 else 0

    # Get current coin info for profitability (dynamic, supports all 14 coins)
    # Use power_cost currency for profitability calculations (or CAD fallback)
    power_currency = power_config.get("currency", "CAD").upper()
    cur_meta = DASHBOARD_CURRENCIES.get(power_currency, DASHBOARD_CURRENCIES["CAD"])
    coin_price = block_reward_cache.get(f"price_{cur_meta['code']}", 0)
    # Fallback to USD if preferred currency has no price data
    if coin_price == 0:
        coin_price = block_reward_cache.get("price_usd", 0)
    block_reward = block_reward_cache.get("block_reward", 0)
    primary_coin = get_enabled_coins().get("primary", None)

    # Estimate daily earnings (rough estimate based on luck tracker)
    estimated_daily_coin = luck_tracker.get("blocks_expected", 0) / 30 * block_reward if block_reward > 0 else 0
    estimated_daily_fiat = estimated_daily_coin * coin_price if coin_price > 0 else 0

    daily_profit = estimated_daily_fiat - power_cost_tracker["daily_cost"]
    monthly_profit = daily_profit * 30

    is_free = power_config.get("is_free_power", False)

    # Build profitability dict with dynamic coin key (if coin is detected)
    profitability = {
        "coin_price_usd": block_reward_cache.get("price_usd", 0),  # backward compat
        "coin_price_fiat": coin_price,
        "profitability_currency": power_currency,
        "coin_symbol": primary_coin,
        "estimated_daily_fiat": round(estimated_daily_fiat, 2),
        "estimated_daily_usd": round(estimated_daily_fiat, 2),  # backward compat alias
        "daily_profit": round(daily_profit, 2),
        "monthly_profit": round(monthly_profit, 2),
        "profit_margin_percent": round((daily_profit / estimated_daily_fiat * 100) if estimated_daily_fiat > 0 else (100 if is_free else 0), 1)
    }
    # Add dynamic coin-specific key only if we have a detected coin
    if primary_coin:
        profitability[f"estimated_daily_{primary_coin.lower()}"] = round(estimated_daily_coin, 2)

    return jsonify({
        "success": True,
        "power": {
            "current_watts": total_watts,
            "current_kw": round(total_watts / 1000, 2),
            "daily_kwh": power_cost_tracker["daily_kwh"],
            "monthly_kwh": power_cost_tracker["monthly_kwh"]
        },
        "cost": {
            "rate_per_kwh": power_config.get("rate_per_kwh", 0.12),
            "currency": power_config.get("currency", "CAD"),
            "currency_symbol": power_config.get("currency_symbol", "$"),
            "daily_cost": power_cost_tracker["daily_cost"],
            "monthly_cost": power_cost_tracker["monthly_cost"],
            "is_free_power": is_free
        },
        "efficiency": {
            "joules_per_th": round(efficiency_jth, 2),
            "hashrate_ths": round(total_hashrate_ths, 2)
        },
        "profitability": profitability,
        "celebration": "Power cost: $0.00/kWh configured (display only - no profit guarantee)" if is_free else None
    })


@app.route('/api/power/efficiency', methods=['GET'])
@api_key_or_login_required
def get_power_efficiency():
    """V1.0: Get per-miner power efficiency metrics"""
    miners_efficiency = []

    for miner_name, miner in miner_cache.get("miners", {}).items():
        if not miner.get("online", False):
            continue
        hashrate = miner.get("hashrate_ths", miner.get("hashrate_ghs", 0) / 1000)
        power = miner.get("power_watts", 0)

        if hashrate > 0 and power > 0:
            efficiency = power / hashrate  # J/TH
        else:
            efficiency = 0

        miners_efficiency.append({
            "ip": miner.get("ip", "unknown"),
            "name": miner.get("name", miner_name),
            "device_type": miner.get("type", "unknown"),
            "hashrate_ths": round(hashrate, 2),
            "power_watts": power,
            "efficiency_jth": round(efficiency, 2)
        })

    # Sort by efficiency (best first = lowest J/TH)
    miners_efficiency.sort(key=lambda x: x["efficiency_jth"] if x["efficiency_jth"] > 0 else float('inf'))

    # Calculate farm average
    total_power = sum(m["power_watts"] for m in miners_efficiency)
    total_hashrate = sum(m["hashrate_ths"] for m in miners_efficiency)
    farm_efficiency = (total_power / total_hashrate) if total_hashrate > 0 else 0

    return jsonify({
        "success": True,
        "miners": miners_efficiency,
        "farm_summary": {
            "total_power_watts": total_power,
            "total_hashrate_ths": round(total_hashrate, 2),
            "average_efficiency_jth": round(farm_efficiency, 2),
            "best_miner": miners_efficiency[0] if miners_efficiency else None,
            "worst_miner": miners_efficiency[-1] if miners_efficiency else None
        }
    })


# ============================================
# V1.0: WEBSOCKET REAL-TIME UPDATES
# ============================================

@socketio.on('connect')
def handle_connect():
    """Handle WebSocket client connection"""
    # SECURITY (F-01): Reject unauthenticated WebSocket connections.
    # Flask-SocketIO does NOT automatically enforce Flask-Login sessions —
    # auth must be checked explicitly here. Returning False disconnects the client.
    if AUTH_ENABLED and not current_user.is_authenticated:
        return False
    with _ws_lock:
        websocket_clients["count"] += 1
    emit('connected', {
        'message': 'Connected to Spiral Pool Dashboard',
        'clients': websocket_clients["count"]
    })


@socketio.on('disconnect')
def handle_disconnect():
    """Handle WebSocket client disconnection"""
    with _ws_lock:
        websocket_clients["count"] = max(0, websocket_clients["count"] - 1)


@socketio.on('subscribe')
def handle_subscribe(data):
    """Subscribe to specific data channels"""
    channels = data.get('channels', ['stats'])
    emit('subscribed', {'channels': channels})


def broadcast_realtime_update():
    """Broadcast real-time updates to all connected WebSocket clients"""
    with _ws_lock:
        if websocket_clients["count"] == 0:
            return

        # Rate limit broadcasts to every 5 seconds
        now = time.time()
        if now - websocket_clients["last_broadcast"] < 5:
            return

        websocket_clients["last_broadcast"] = now

    # Compile real-time data — read caches under their respective locks
    with _pool_stats_lock:
        pool_data = {
            "hashrate_ths": pool_stats_cache.get("pool_hashrate", 0) / 1e12,
            "connected_miners": pool_stats_cache.get("connected_miners", 0),
            "shares_per_second": pool_stats_cache.get("shares_per_second", 0),
            "blocks_found": pool_stats_cache.get("blocks_found", 0)
        }

    with _miner_cache_lock:
        farm_data = {
            "hashrate_ths": miner_cache.get("totals", {}).get("hashrate_ths", 0),
            "power_watts": miner_cache.get("totals", {}).get("power_watts", 0),
            "miners_online": len([m for m in miner_cache.get("miners", {}).values() if isinstance(m, dict) and m.get("online", False)])
        }

    update_data = {
        "timestamp": now,
        "pool": pool_data,
        "farm": farm_data,
        "session": {
            "uptime_seconds": now - session_stats["start_time"],
            "shares_accepted": session_stats["shares_accepted"],
            "blocks_found": session_stats["blocks_found"]
        },
        "etb": {
            "estimated_seconds": etb_calculator.get("estimated_seconds", 0),
            "probability_24h": etb_calculator.get("probability_24h", 0)
        }
    }

    # Include HA status if HA is enabled (allows real-time failover visibility)
    if ha_status_cache.get("enabled"):
        update_data["ha"] = {
            "local_role": ha_status_cache.get("local_role", "UNKNOWN"),
            "state": ha_status_cache.get("state", "unknown"),
            "vip": ha_status_cache.get("vip", ""),
            "healthy_nodes": ha_status_cache.get("healthy_nodes", 0),
            "node_count": ha_status_cache.get("node_count", 0)
        }

    socketio.emit('realtime_update', update_data)


def broadcast_block_found(block_data):
    """Broadcast when a block is found - MASSIVE celebration!"""
    quiet = _is_celebration_quiet_hours()
    socketio.emit('block_found', {
        "timestamp": time.time(),
        "block": block_data,
        "celebration": not quiet,
        "message": "🎉🎉🎉 BLOCK FOUND! 🎉🎉🎉"
    })

    # Record to activity feed
    coin = block_data.get("coin", "")
    height = block_data.get("height", "?")
    finder = block_data.get("source") or block_data.get("worker") or block_data.get("miner") or "unknown"
    record_activity("block", f"Block found by {finder} ({coin} #{height})", block_data)

    # Persist block finder attribution
    try:
        record_block_finder(
            block_hash=block_data.get("hash", ""),
            block_height=height,
            worker_name=finder,
        )
    except Exception:
        pass

    # Trigger LED celebration on all Avalon miners (skip during quiet hours)
    if quiet:
        print("[BLOCK] LED + browser celebration suppressed — quiet hours active")
    else:
        trigger_avalon_block_celebration()


_celebration_cancel = threading.Event()   # Cancel signal for active LED celebrations
_celebration_active = False                # Guard against thread explosion


def _is_celebration_quiet_hours():
    """Check if celebrations should be suppressed right now.

    Returns True if EITHER condition is met:
    1. Clock-based quiet hours (from Sentinel config, default 22:00-06:00)
    2. Any Avalon miner has an enabled schedule with 'efficiency' profile active

    When True, LED celebrations and browser celebrations are suppressed.
    Text notifications (Discord/XMPP/Telegram) are NOT affected.
    """
    # Check clock-based quiet hours (Sentinel config)
    try:
        quiet_start = 22  # default
        quiet_end = 6     # default
        tz_name = "America/New_York"  # default — matches install.sh DISPLAY_TIMEZONE default
        install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
        sentinel_paths = [
            Path(install_dir) / "config" / "sentinel" / "config.json",
            Path.home() / ".spiralsentinel" / "config.json",
        ]
        for p in sentinel_paths:
            if p.exists():
                with open(p, 'r') as f:
                    sentinel_cfg = json.load(f)
                quiet_start = sentinel_cfg.get("quiet_hours_start", 22)
                quiet_end = sentinel_cfg.get("quiet_hours_end", 6)
                tz_name = sentinel_cfg.get("display_timezone", "America/New_York")
                break

        # Use configured display timezone so quiet hours match the user's local
        # time, not the server's UTC clock (server always runs UTC).
        try:
            import zoneinfo
            tz = zoneinfo.ZoneInfo(tz_name)
            h = datetime.now(tz).hour
        except Exception:
            h = datetime.now().hour
        if quiet_start < quiet_end:
            if quiet_start <= h < quiet_end:
                return True
        else:
            if h >= quiet_start or h < quiet_end:
                return True
    except Exception:
        pass

    # Check Avalon power schedule (efficiency = quiet)
    try:
        if avalon_schedules:
            current_time = datetime.now().time()
            for ip, schedule in avalon_schedules.items():
                if not schedule.get("enabled", False):
                    continue
                active_profile = get_active_profile_for_time(schedule, current_time)
                if active_profile == "efficiency":
                    return True
    except Exception:
        pass
    return False


def trigger_avalon_block_celebration():
    """Flash LEDs on all Avalon miners to celebrate a block find.

    Uses CGMiner ascset command to trigger LED pattern.
    Avalon devices support LED control via:
    - ascset|0,led,1 - Turn on LED
    - ascset|0,led,0 - Turn off LED

    NOTE: Avalon LEDs are single-color (no RGB support), but we create
    exciting patterns with varied timing to make it unmistakable!

    Celebration runs for 1 HOUR with 10 rotating eye-catching patterns!
    Anyone walking by will know you found a block.

    This triggers for ANY miner finding a block - the Avalon celebrates
    for the whole fleet!

    Thread-safe: cancels any active celebration before starting a new one
    to prevent unbounded thread growth from rapid block finds.
    """
    global _celebration_active
    import random

    # Cancel any active celebration before starting a new one
    _celebration_cancel.set()
    time.sleep(0.2)  # Brief pause for threads to notice cancellation
    _celebration_cancel.clear()
    _celebration_active = True

    # Duration: 1 hour = 3600 seconds
    CELEBRATION_DURATION = 3600

    def led_on(ip, port):
        cgminer_command(ip, port, "ascset", "0,led,1", timeout=2)

    def led_off(ip, port):
        cgminer_command(ip, port, "ascset", "0,led,0", timeout=2)

    def celebration_sequence(ip, port=4028):
        """Run LED celebration sequence for a single Avalon miner."""
        try:
            start_time = time.time()
            end_time = start_time + CELEBRATION_DURATION
            pattern_num = 0

            # Celebrate for 1 hour with 10 rotating patterns!
            # Check cancel event to allow early termination on new block
            while time.time() < end_time and not _celebration_cancel.is_set():
                pattern_num = (pattern_num + 1) % 10

                if pattern_num == 0:
                    # Pattern 1: RAVE MODE - Super fast strobe (20 flashes)
                    for _ in range(20):
                        led_on(ip, port)
                        time.sleep(0.05)
                        led_off(ip, port)
                        time.sleep(0.05)

                elif pattern_num == 1:
                    # Pattern 2: Heartbeat - thump-thump... thump-thump...
                    for _ in range(4):
                        led_on(ip, port)
                        time.sleep(0.1)
                        led_off(ip, port)
                        time.sleep(0.1)
                        led_on(ip, port)
                        time.sleep(0.1)
                        led_off(ip, port)
                        time.sleep(0.6)

                elif pattern_num == 2:
                    # Pattern 3: Slow majestic pulse (winner's glow)
                    for _ in range(3):
                        led_on(ip, port)
                        time.sleep(1.5)
                        led_off(ip, port)
                        time.sleep(0.5)

                elif pattern_num == 3:
                    # Pattern 4: Morse code "BLOCK" (B=-... L=.-.. O=--- C=-.-. K=-.-)
                    morse_block = [
                        [0.4, 0.1, 0.1, 0.1],  # B: -...
                        [0.1, 0.4, 0.1, 0.1],  # L: .-..
                        [0.4, 0.4, 0.4],       # O: ---
                        [0.4, 0.1, 0.4, 0.1],  # C: -.-.
                        [0.4, 0.1, 0.4],       # K: -.-
                    ]
                    for letter in morse_block:
                        for duration in letter:
                            led_on(ip, port)
                            time.sleep(duration)
                            led_off(ip, port)
                            time.sleep(0.1)
                        time.sleep(0.3)  # Letter gap

                elif pattern_num == 4:
                    # Pattern 5: Accelerating pulse (builds excitement)
                    delays = [0.5, 0.4, 0.3, 0.2, 0.15, 0.1, 0.08, 0.05, 0.05, 0.05]
                    for delay in delays:
                        led_on(ip, port)
                        time.sleep(delay)
                        led_off(ip, port)
                        time.sleep(delay)
                    # Then slow down
                    for delay in reversed(delays):
                        led_on(ip, port)
                        time.sleep(delay)
                        led_off(ip, port)
                        time.sleep(delay)

                elif pattern_num == 5:
                    # Pattern 6: Party mode - random timing!
                    for _ in range(15):
                        led_on(ip, port)
                        time.sleep(random.uniform(0.05, 0.3))
                        led_off(ip, port)
                        time.sleep(random.uniform(0.05, 0.2))

                elif pattern_num == 6:
                    # Pattern 7: SOS (... --- ...) - attention signal
                    # S = ...
                    for _ in range(3):
                        led_on(ip, port)
                        time.sleep(0.1)
                        led_off(ip, port)
                        time.sleep(0.1)
                    time.sleep(0.3)
                    # O = ---
                    for _ in range(3):
                        led_on(ip, port)
                        time.sleep(0.4)
                        led_off(ip, port)
                        time.sleep(0.15)
                    time.sleep(0.3)
                    # S = ...
                    for _ in range(3):
                        led_on(ip, port)
                        time.sleep(0.1)
                        led_off(ip, port)
                        time.sleep(0.1)

                elif pattern_num == 7:
                    # Pattern 8: Double flash burst
                    for _ in range(5):
                        led_on(ip, port)
                        time.sleep(0.1)
                        led_off(ip, port)
                        time.sleep(0.1)
                        led_on(ip, port)
                        time.sleep(0.1)
                        led_off(ip, port)
                        time.sleep(0.5)

                elif pattern_num == 8:
                    # Pattern 9: Long ON with quick interrupts (lightning)
                    for _ in range(3):
                        led_on(ip, port)
                        time.sleep(1.0)
                        led_off(ip, port)
                        time.sleep(0.05)
                        led_on(ip, port)
                        time.sleep(0.05)
                        led_off(ip, port)
                        time.sleep(0.05)
                        led_on(ip, port)
                        time.sleep(0.05)
                        led_off(ip, port)
                        time.sleep(0.3)

                else:
                    # Pattern 10: Wave effect (speed oscillation)
                    for speed in [0.3, 0.25, 0.2, 0.15, 0.1, 0.15, 0.2, 0.25, 0.3]:
                        led_on(ip, port)
                        time.sleep(speed)
                        led_off(ip, port)
                        time.sleep(speed * 0.5)

                # Brief pause between patterns
                time.sleep(1.0)

            # Celebration over (or cancelled) - turn LED off
            led_off(ip, port)

        except Exception as e:
            # Silently fail - LED celebration is nice-to-have, not critical
            pass
        finally:
            global _celebration_active
            _celebration_active = False

    # Get all Avalon miners from config
    try:
        config = load_config()
        avalon_devices = config.get("devices", {}).get("avalon", [])

        # Run celebration on each Avalon in a separate thread (non-blocking)
        # Previous celebration was already cancelled above, so at most
        # len(avalon_devices) threads run at once (typically 1-10 miners)
        for device in avalon_devices:
            ip = device.get("ip")
            port = device.get("port", 4028)
            if ip:
                thread = threading.Thread(
                    target=celebration_sequence,
                    args=(ip, port),
                    daemon=True
                )
                thread.start()

    except Exception as e:
        # Config load failed - silently continue
        pass


def broadcast_alert(alert_data):
    """Broadcast alerts to connected clients"""
    socketio.emit('alert', alert_data)


# ============================================
# NOTE: HASHRATE WATCHDOG WITH AUTO-RESTART
# is handled by Spiral Sentinel (SpiralSentinel.py)
# Features: zombie detection, auto-restart, cooldowns, notifications
# ============================================


# ============================================
# V1.0: SHARE AUDIT LOG / PROOF TRAIL
# ============================================

def record_share_audit(miner_ip, worker, difficulty, hash_prefix, accepted, block_candidate=False):
    """Record a share to the audit log with timestamp and index"""
    global share_audit_log, session_stats

    now = time.time()
    share_audit_log["last_index"] += 1
    share_audit_log["total_shares"] += 1

    entry = {
        "index": share_audit_log["last_index"],
        "timestamp": now,
        "datetime": datetime.utcfromtimestamp(now).isoformat() + "Z",  # UTC ISO format
        "miner_ip": miner_ip,
        "worker": worker,
        "difficulty": difficulty,
        "hash_prefix": hash_prefix[:16] if hash_prefix else None,  # First 16 chars for verification
        "accepted": accepted,
        "block_candidate": block_candidate
    }

    share_audit_log["shares"].append(entry)

    # Update session stats
    session_stats["shares_submitted"] += 1
    if accepted:
        session_stats["shares_accepted"] += 1
    else:
        session_stats["shares_rejected"] += 1

    if difficulty > session_stats["best_share_difficulty"]:
        session_stats["best_share_difficulty"] = difficulty

    # Rotate if over max entries
    if len(share_audit_log["shares"]) > share_audit_log["max_entries"]:
        # Keep last 90% of entries
        keep_count = int(share_audit_log["max_entries"] * 0.9)
        share_audit_log["shares"] = share_audit_log["shares"][-keep_count:]

    # Also record to heatmap
    record_share_to_heatmap()


def load_share_audit_log():
    """Load share audit log from disk"""
    global share_audit_log

    if SHARE_AUDIT_FILE.exists():
        try:
            with open(SHARE_AUDIT_FILE, 'r') as f:
                data = json.load(f)
                share_audit_log["shares"] = data.get("shares", [])[-share_audit_log["max_entries"]:]
                share_audit_log["total_shares"] = data.get("total_shares", 0)
                share_audit_log["last_index"] = data.get("last_index", 0)
        except Exception as e:
            print(f"Error loading share audit log: {e}")


def save_share_audit_log():
    """Save share audit log to disk"""
    try:
        _atomic_json_save(str(SHARE_AUDIT_FILE), {
            "shares": share_audit_log["shares"][-share_audit_log["max_entries"]:],
            "total_shares": share_audit_log["total_shares"],
            "last_index": share_audit_log["last_index"],
            "session_start": share_audit_log["session_start"]
        })
    except Exception as e:
        print(f"Error saving share audit log: {e}")


@app.route('/api/shares/audit', methods=['GET'])
@api_key_or_login_required
def get_share_audit():
    """V1.0: Get share audit log with proof of work trail"""
    try:
        limit = int(request.args.get("limit", 100))
        offset = int(request.args.get("offset", 0))
    except (ValueError, TypeError):
        return jsonify({"success": False, "error": "Invalid limit or offset parameter"}), 400
    miner_ip = request.args.get("miner_ip")
    accepted_only = request.args.get("accepted_only", "false").lower() == "true"

    shares = share_audit_log["shares"]

    # Filter if needed
    if miner_ip:
        shares = [s for s in shares if s.get("miner_ip") == miner_ip]
    if accepted_only:
        shares = [s for s in shares if s.get("accepted", False)]

    # Apply pagination (from end, most recent first)
    total = len(shares)
    shares = list(reversed(shares))  # Most recent first
    shares = shares[offset:offset + limit]

    return jsonify({
        "success": True,
        "shares": shares,
        "pagination": {
            "total": total,
            "offset": offset,
            "limit": limit,
            "has_more": offset + limit < total
        },
        "summary": {
            "total_shares_logged": share_audit_log["total_shares"],
            "session_start": share_audit_log["session_start"],
            "last_index": share_audit_log["last_index"]
        }
    })


@app.route('/api/shares/audit/export', methods=['GET'])
@api_key_or_login_required
def export_share_audit():
    """V1.0: Export share audit log as CSV for verification"""
    import io

    output = io.StringIO()
    output.write("index,timestamp,datetime,miner_ip,worker,difficulty,hash_prefix,accepted,block_candidate\n")

    for share in share_audit_log["shares"]:
        output.write(f"{share.get('index', 0)},{share.get('timestamp', 0)},{csv_safe(share.get('datetime', ''))},")
        output.write(f"{csv_safe(share.get('miner_ip', ''))},{csv_safe(share.get('worker', ''))},{share.get('difficulty', 0)},")
        output.write(f"{csv_safe(share.get('hash_prefix', ''))},{share.get('accepted', False)},{share.get('block_candidate', False)}\n")

    response = app.response_class(
        response=output.getvalue(),
        status=200,
        mimetype='text/csv'
    )
    response.headers["Content-Disposition"] = f"attachment; filename=spiralpool_share_audit_{int(time.time())}.csv"
    return response


# ============================================
# V1.0: ESTIMATED TIME TO BLOCK (ETB)
# ============================================

def update_etb_calculation():
    """Update Estimated Time to Block calculation"""
    global etb_calculator

    now = time.time()

    # Rate limit to every 30 seconds
    if now - etb_calculator["last_update"] < 30:
        return

    # Get current farm hashrate
    farm_hashrate_ths = miner_cache.get("totals", {}).get("hashrate_ths", 0)
    if farm_hashrate_ths <= 0:
        return

    # Get network difficulty from pool stats
    network_difficulty = pool_stats_cache.get("network_difficulty", 0)
    if network_difficulty <= 0:
        # Try to get from node using proper multi-algo extraction
        try:
            coins = get_enabled_coins()
            primary_coin = coins.get("primary")  # No default - use detected coin
            if primary_coin:
                network_difficulty = get_sha256_difficulty(primary_coin)
        except (requests.exceptions.RequestException, ValueError, KeyError, TypeError):
            pass

    if network_difficulty <= 0:
        return

    etb_calculator["current_hashrate_ths"] = farm_hashrate_ths
    etb_calculator["network_difficulty"] = network_difficulty

    # Convert to hashes per second
    farm_hashrate_hs = farm_hashrate_ths * 1e12

    # Expected hashes per block = difficulty * 2^32 (SHA256d) or 2^16 (Scrypt)
    coins = get_enabled_coins()
    algo = get_algorithm_for_coin(coins.get("primary", "BTC"))
    diff_multiplier = (2 ** 16) if algo == "scrypt" else (2 ** 32)
    expected_hashes_per_block = network_difficulty * diff_multiplier

    # Time to find one block = expected_hashes / hashrate
    if farm_hashrate_hs > 0:
        estimated_seconds = expected_hashes_per_block / farm_hashrate_hs
    else:
        estimated_seconds = float('inf')

    etb_calculator["estimated_seconds"] = estimated_seconds

    # Calculate probabilities for different time periods
    # P(at least 1 block) = 1 - e^(-time/expected_time)
    import math

    if estimated_seconds > 0 and estimated_seconds != float('inf'):
        # Probability of finding at least one block in 24 hours
        etb_calculator["probability_24h"] = round((1 - math.exp(-86400 / estimated_seconds)) * 100, 4)
        # 7 days
        etb_calculator["probability_7d"] = round((1 - math.exp(-604800 / estimated_seconds)) * 100, 4)
        # 30 days
        etb_calculator["probability_30d"] = round((1 - math.exp(-2592000 / estimated_seconds)) * 100, 4)
    else:
        etb_calculator["probability_24h"] = 0
        etb_calculator["probability_7d"] = 0
        etb_calculator["probability_30d"] = 0

    etb_calculator["last_update"] = now


@app.route('/api/etb', methods=['GET'])
@api_key_or_login_required
def get_estimated_time_to_block():
    """V1.0: Get Estimated Time to Block calculation"""
    update_etb_calculation()

    estimated_seconds = etb_calculator.get("estimated_seconds", 0)
    hashrate_ths = etb_calculator.get("current_hashrate_ths", 0)
    network_diff = etb_calculator.get("network_difficulty", 0)

    # Sanity check: ETB under 5 minutes is likely wrong difficulty or hashrate values
    sanity_warning = None
    if estimated_seconds > 0 and estimated_seconds < 300:
        sanity_warning = (
            f"WARNING: ETB of {estimated_seconds/60:.1f} minutes is unrealistically low. "
            f"Check if network_difficulty ({network_diff:.2e}) and hashrate ({hashrate_ths:.2f} TH/s) are correct. "
            f"For DGB, ensure you're getting SHA-256d difficulty, not another algorithm."
        )

    # Format time nicely
    if estimated_seconds == float('inf') or estimated_seconds <= 0:
        time_formatted = "∞ (need more hashrate)"
    elif estimated_seconds < 300:
        time_formatted = f"{estimated_seconds/60:.1f} minutes (check data!)"
    elif estimated_seconds < 3600:
        time_formatted = f"{estimated_seconds/60:.0f} minutes"
    elif estimated_seconds < 86400:
        time_formatted = f"{estimated_seconds/3600:.1f} hours"
    elif estimated_seconds < 604800:
        time_formatted = f"{estimated_seconds/86400:.1f} days"
    elif estimated_seconds < 2592000:
        time_formatted = f"{estimated_seconds/604800:.1f} weeks"
    elif estimated_seconds < 31536000:
        time_formatted = f"{estimated_seconds/2592000:.1f} months"
    else:
        time_formatted = f"{estimated_seconds/31536000:.1f} years"

    response = {
        "success": True,
        "etb": {
            "estimated_seconds": round(estimated_seconds, 0) if estimated_seconds != float('inf') else None,
            "estimated_formatted": time_formatted,
            "current_hashrate_ths": round(hashrate_ths, 2),
            "network_difficulty": network_diff
        },
        "probability": {
            "24h": etb_calculator.get("probability_24h", 0),
            "7d": etb_calculator.get("probability_7d", 0),
            "30d": etb_calculator.get("probability_30d", 0)
        },
        "note": "Probabilities based on Poisson distribution. Mining is random - you could find a block in 1 minute or 1 year!",
        "debug": {
            "pool_stats_difficulty": pool_stats_cache.get("network_difficulty", 0),
            "hashrate_source": "pool" if pool_stats_cache.get("pool_hashrate", 0) > 0 else "farm"
        }
    }

    if sanity_warning:
        response["warning"] = sanity_warning

    return jsonify(response)


# ============================================
# V1.0: SESSION STATISTICS
# ============================================

def update_session_stats():
    """Update session statistics"""
    global session_stats

    # Update peak hashrate
    current_hashrate = miner_cache.get("totals", {}).get("hashrate_ths", 0)
    if current_hashrate > session_stats["peak_hashrate_ths"]:
        session_stats["peak_hashrate_ths"] = current_hashrate

    # Update max miners connected
    current_miners = len(miner_cache.get("miners", {}))
    if current_miners > session_stats["miners_connected_max"]:
        session_stats["miners_connected_max"] = current_miners


@app.route('/api/session/stats', methods=['GET'])
@api_key_or_login_required
def get_session_stats():
    """V1.0: Get statistics for current session (since dashboard restart)"""
    update_session_stats()

    now = time.time()
    uptime_seconds = now - session_stats["start_time"]

    # Format uptime
    days = int(uptime_seconds // 86400)
    hours = int((uptime_seconds % 86400) // 3600)
    minutes = int((uptime_seconds % 3600) // 60)

    if days > 0:
        uptime_formatted = f"{days}d {hours}h {minutes}m"
    elif hours > 0:
        uptime_formatted = f"{hours}h {minutes}m"
    else:
        uptime_formatted = f"{minutes}m"

    # Calculate shares per minute
    shares_per_minute = session_stats["shares_accepted"] / (uptime_seconds / 60) if uptime_seconds > 60 else 0

    # Calculate acceptance rate
    total_shares = session_stats["shares_accepted"] + session_stats["shares_rejected"]
    acceptance_rate = (session_stats["shares_accepted"] / total_shares * 100) if total_shares > 0 else 100

    return jsonify({
        "success": True,
        "session": {
            "start_time": session_stats["start_time"],
            "uptime_seconds": round(uptime_seconds),
            "uptime_formatted": uptime_formatted
        },
        "shares": {
            "submitted": session_stats["shares_submitted"],
            "accepted": session_stats["shares_accepted"],
            "rejected": session_stats["shares_rejected"],
            "acceptance_rate": round(acceptance_rate, 2),
            "shares_per_minute": round(shares_per_minute, 2)
        },
        "mining": {
            "blocks_found": session_stats["blocks_found"],
            "best_share_difficulty": session_stats["best_share_difficulty"],
            "peak_hashrate_ths": round(session_stats["peak_hashrate_ths"], 2),
            "miners_connected_max": session_stats["miners_connected_max"]
        },
        "watchdog": {
            "restarts_triggered": session_stats["restarts_triggered"]
        }
    })


@app.route('/api/session/reset', methods=['POST'])
@admin_required
def reset_session_stats():
    """V1.0: Reset session statistics (for new session)"""
    global session_stats

    session_stats = {
        "start_time": time.time(),
        "shares_submitted": 0,
        "shares_accepted": 0,
        "shares_rejected": 0,
        "blocks_found": 0,
        "best_share_difficulty": 0,
        "total_hashrate_samples": [],
        "peak_hashrate_ths": 0,
        "miners_connected_max": 0,
        "restarts_triggered": 0
    }

    return jsonify({"success": True, "message": "Session statistics reset"})


# ============================================
# V1.0: LUCK TRACKER
# Track actual vs expected blocks found
# ============================================

def update_luck_tracker():
    """Update luck calculation based on actual vs expected blocks"""
    global luck_tracker

    now = time.time()

    # Rate limit to every 60 seconds
    if now - luck_tracker["last_update"] < 60:
        return

    # Get blocks found from pool stats
    blocks_found = pool_stats_cache.get("blocks_found", 0)
    if blocks_found <= 0:
        return

    # Calculate expected blocks based on hashrate and time
    farm_hashrate_ths = miner_cache.get("totals", {}).get("hashrate_ths", 0)
    network_difficulty = pool_stats_cache.get("network_difficulty", 0)

    if farm_hashrate_ths <= 0 or network_difficulty <= 0:
        return

    # How long have we been mining? Use session start or lifetime start
    mining_duration = now - session_stats.get("start_time", now)
    if mining_duration < 3600:  # Need at least 1 hour of data
        return

    # Expected blocks = (hashrate * time) / (difficulty * diff_multiplier)
    farm_hashrate_hs = farm_hashrate_ths * 1e12
    coins = get_enabled_coins()
    algo = get_algorithm_for_coin(coins.get("primary", "BTC"))
    diff_multiplier = (2 ** 16) if algo == "scrypt" else (2 ** 32)
    expected_hashes_per_block = network_difficulty * diff_multiplier

    if expected_hashes_per_block > 0:
        # Expected blocks in our mining duration
        blocks_expected = (farm_hashrate_hs * mining_duration) / expected_hashes_per_block
    else:
        blocks_expected = 0

    luck_tracker["blocks_found"] = blocks_found
    luck_tracker["blocks_expected"] = round(blocks_expected, 4)

    # Calculate luck percentage (100% = exactly as expected)
    # >100% = lucky (found more than expected)
    # <100% = unlucky (found fewer than expected)
    if blocks_expected > 0:
        luck_tracker["luck_percent"] = round((blocks_found / blocks_expected) * 100, 2)
    else:
        luck_tracker["luck_percent"] = 100.0

    # Update history (keep last 30 days worth of hourly samples)
    luck_tracker["luck_history"].append({
        "timestamp": now,
        "blocks_found": blocks_found,
        "blocks_expected": round(blocks_expected, 4),
        "luck_percent": luck_tracker["luck_percent"]
    })
    luck_tracker["luck_history"] = luck_tracker["luck_history"][-720:]  # 30 days * 24 hours

    luck_tracker["last_update"] = now


@app.route('/api/luck', methods=['GET'])
@api_key_or_login_required
def get_luck_overview():
    """V1.0: Get mining luck statistics"""
    update_luck_tracker()

    luck = luck_tracker["luck_percent"]

    # Determine luck status message
    if luck >= 200:
        status = "Extremely Lucky! 🍀🍀🍀"
        status_emoji = "🍀"
    elif luck >= 150:
        status = "Very Lucky! 🍀🍀"
        status_emoji = "🍀"
    elif luck >= 110:
        status = "Lucky 🍀"
        status_emoji = "🍀"
    elif luck >= 90:
        status = "Normal (as expected)"
        status_emoji = "📊"
    elif luck >= 75:
        status = "Slightly Unlucky"
        status_emoji = "😐"
    elif luck >= 50:
        status = "Unlucky 😔"
        status_emoji = "😔"
    else:
        status = "Very Unlucky 😢"
        status_emoji = "😢"

    return jsonify({
        "success": True,
        "luck": {
            "percent": luck_tracker["luck_percent"],
            "status": status,
            "status_emoji": status_emoji,
            "blocks_found": luck_tracker["blocks_found"],
            "blocks_expected": luck_tracker["blocks_expected"]
        },
        "history": luck_tracker["luck_history"][-24:],  # Last 24 hours
        "note": "Luck >100% means you're finding more blocks than expected. Mining is random - short-term luck varies greatly!"
    })


# ============================================
# V1.0: DIFFICULTY ADJUSTMENT PREDICTOR
# Estimate SHA256 difficulty trend for DigiByte
# ============================================

def update_difficulty_predictor():
    """Update difficulty prediction based on recent trends"""
    global difficulty_predictor

    now = time.time()

    # Rate limit to every 5 minutes
    if now - difficulty_predictor["last_update"] < 300:
        return

    # Get current difficulty
    current_difficulty = pool_stats_cache.get("network_difficulty", 0)
    if current_difficulty <= 0:
        # Try from node using proper multi-algo extraction
        try:
            coins = get_enabled_coins()
            primary_coin = coins.get("primary")  # No default - use detected coin
            if primary_coin:
                current_difficulty = get_sha256_difficulty(primary_coin)
        except (requests.exceptions.RequestException, ValueError, KeyError, TypeError):
            pass

    if current_difficulty <= 0:
        return

    # Store previous for comparison
    if difficulty_predictor["current_difficulty"] > 0:
        difficulty_predictor["previous_difficulty"] = difficulty_predictor["current_difficulty"]

    difficulty_predictor["current_difficulty"] = current_difficulty

    # Record to history
    difficulty_predictor["difficulty_history"].append({
        "timestamp": now,
        "difficulty": current_difficulty
    })
    # Keep last 7 days of history (every 5 min = 2016 samples)
    difficulty_predictor["difficulty_history"] = difficulty_predictor["difficulty_history"][-2016:]

    # Calculate trend from last 6 hours of data
    six_hours_ago = now - 21600
    recent_samples = [d for d in difficulty_predictor["difficulty_history"] if d["timestamp"] > six_hours_ago]

    if len(recent_samples) >= 2:
        # Linear regression to find trend
        first_diff = recent_samples[0]["difficulty"]
        last_diff = recent_samples[-1]["difficulty"]

        if first_diff > 0:
            change_percent = ((last_diff - first_diff) / first_diff) * 100

            # Predict next difficulty (simple linear extrapolation)
            # DigiByte adjusts difficulty every block with DigiShield
            difficulty_predictor["predicted_change_percent"] = round(change_percent, 2)

            if change_percent > 5:
                difficulty_predictor["trend"] = "increasing"
                difficulty_predictor["predicted_next_difficulty"] = current_difficulty * 1.05
            elif change_percent < -5:
                difficulty_predictor["trend"] = "decreasing"
                difficulty_predictor["predicted_next_difficulty"] = current_difficulty * 0.95
            else:
                difficulty_predictor["trend"] = "stable"
                difficulty_predictor["predicted_next_difficulty"] = current_difficulty

    difficulty_predictor["last_update"] = now


@app.route('/api/difficulty/predict', methods=['GET'])
@api_key_or_login_required
def get_difficulty_prediction():
    """V1.0: Get difficulty prediction and trend analysis"""
    update_difficulty_predictor()

    current = difficulty_predictor["current_difficulty"]
    predicted = difficulty_predictor["predicted_next_difficulty"]

    # Format difficulty for display
    if current >= 1e12:
        current_formatted = f"{current/1e12:.2f}T"
    elif current >= 1e9:
        current_formatted = f"{current/1e9:.2f}G"
    elif current >= 1e6:
        current_formatted = f"{current/1e6:.2f}M"
    else:
        current_formatted = f"{current:.2f}"

    trend = difficulty_predictor["trend"]
    if trend == "increasing":
        trend_emoji = "📈"
        impact = "Mining will become harder - expect longer time between blocks"
    elif trend == "decreasing":
        trend_emoji = "📉"
        impact = "Mining will become easier - expect shorter time between blocks"
    else:
        trend_emoji = "➡️"
        impact = "Difficulty is stable - consistent mining conditions"

    return jsonify({
        "success": True,
        "difficulty": {
            "current": current,
            "current_formatted": current_formatted,
            "previous": difficulty_predictor["previous_difficulty"],
            "predicted_next": round(predicted, 2),
            "predicted_change_percent": difficulty_predictor["predicted_change_percent"]
        },
        "trend": {
            "direction": trend,
            "emoji": trend_emoji,
            "impact": impact
        },
        "history": difficulty_predictor["difficulty_history"][-72:],  # Last 6 hours
        "note": "DigiByte uses DigiShield which adjusts difficulty every block based on recent block times"
    })


# ============================================
# MAIN
# ============================================

def cli_reset_devices():
    """Reset device list to empty defaults while preserving all other config.

    Preserves: pool_admin_api_key, auth, dashboard_title, power_cost, theme,
               refresh_interval, lifetime stats, block history.
    Removes:   All device entries (including orphaned keys like 'nerdminer'),
               miner groups, in-memory caches.
    """
    ensure_config_dir()
    if not CONFIG_FILE.exists():
        print("[RESET] No config file found — nothing to reset.")
        return

    with open(CONFIG_FILE, 'r') as f:
        config = json.load(f)

    # Count existing devices
    old_devices = config.get("devices", {})
    total = sum(len(v) for v in old_devices.values() if isinstance(v, list))
    orphaned_keys = [k for k in old_devices if k not in DEFAULT_CONFIG.get("devices", {})]

    if orphaned_keys:
        print(f"[RESET] Found orphaned device type keys: {orphaned_keys}")
        for key in orphaned_keys:
            entries = old_devices.get(key, [])
            if isinstance(entries, list):
                for entry in entries:
                    print(f"  - {key}: {entry.get('name', '?')} @ {entry.get('ip', '?')}")

    # Replace devices with clean defaults
    config["devices"] = copy.deepcopy(DEFAULT_CONFIG["devices"])

    # Save
    with open(CONFIG_FILE, 'w') as f:
        json.dump(config, f, indent=2)

    print(f"[RESET] Cleared {total} device(s) across {len(old_devices)} type(s).")
    if orphaned_keys:
        print(f"[RESET] Removed {len(orphaned_keys)} orphaned key(s): {orphaned_keys}")
    print("[RESET] Device list reset to empty defaults.")
    print("[RESET] Preserved: pool_admin_api_key, auth, dashboard_title, power_cost, theme")
    print("[RESET] Restart the dashboard and re-scan your network to re-discover devices.")


def cli_reset_stats():
    """Reset lifetime and session statistics while preserving config."""
    ensure_config_dir()
    stats_file = CONFIG_DIR / "dashboard_stats.json"
    if stats_file.exists():
        stats_file.rename(stats_file.with_suffix('.json.bak'))
        print("[RESET] Backed up and removed dashboard_stats.json")
    else:
        print("[RESET] No stats file found.")
    print("[RESET] Stats will be re-initialized on next startup.")


def cli_reset_all():
    """Full factory reset — wipes devices, stats, and auth.

    WARNING: This removes the admin password. The API key is preserved in
    /spiralpool/config/config.yaml and will be re-read on next startup.
    """
    ensure_config_dir()

    backed_up = []
    for fname in ["dashboard_config.json", "dashboard_stats.json", "auth.json"]:
        fpath = CONFIG_DIR / fname
        if fpath.exists():
            fpath.rename(fpath.with_suffix('.json.bak'))
            backed_up.append(fname)

    if backed_up:
        print(f"[RESET] Backed up and removed: {', '.join(backed_up)}")
    else:
        print("[RESET] No files to reset.")

    print("[RESET] Full factory reset complete.")
    print("[RESET] API key will be re-read from /spiralpool/config/config.yaml on next startup.")
    print("[RESET] You will need to set a new admin password on first login.")


# Module-level initialization: load persistent state so gunicorn workers
# have lifetime stats, block history, etc. available immediately.
# (Under __main__ these are called again, but load_stats() is idempotent.)
load_stats()
load_block_finder_history()
load_miner_groups()
load_historical_data()
load_activity_feed()
load_share_audit_log()

# CRITICAL FIX: Start background data collection at module level so it runs
# under gunicorn (where __name__ == "dashboard", NOT "__main__").
# Without this, record_historical_data() / save_historical_data() never run,
# the hashrate chart is always empty, and alerts/ETB/luck never update.
# Guard prevents double-start when running directly via `python dashboard.py`.
_bg_thread_started = False

def _ensure_background_thread():
    global _bg_thread_started
    if _bg_thread_started:
        return
    _bg_thread_started = True
    ensure_config_dir()
    if not lifetime_stats.get("uptime_start"):
        lifetime_stats["uptime_start"] = time.time()
        save_stats()
    collector = threading.Thread(target=background_data_collection, daemon=True)
    collector.start()

    # BUGFIX: Sync miners to sentinel on startup.  sync_miners_to_sentinel() had a
    # bug (type_mapping used before definition) that meant miners NEVER reached
    # miners.json.  Now that it's fixed, sync at startup so existing dashboard
    # miners are immediately available to the sentinel without manual intervention.
    try:
        config = load_config()
        has_devices = any(
            len(config.get("devices", {}).get(dtype, [])) > 0
            for dtype in config.get("devices", {})
        )
        if has_devices:
            synced, errs = sync_miners_to_sentinel()
            print(f"[STARTUP] Synced {synced} miners to sentinel database")
            if errs:
                print(f"[STARTUP] Sync errors: {errs}")
    except Exception as e:
        print(f"[STARTUP] Failed to sync miners on startup: {e}")

_ensure_background_thread()

if __name__ == '__main__':
    parser = argparse.ArgumentParser(
        description="Spiral Pool Dashboard - Cyberpunk Mining Monitor",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
reset commands:
  --reset-devices    Clear all device entries and re-scan on next startup.
                     Preserves API key, auth, settings, and stats.
  --reset-stats      Clear lifetime and session statistics.
  --reset-all        Factory reset (devices + stats + auth password).
                     API key is preserved in config.yaml.
        """,
    )
    parser.add_argument('--reset-devices', action='store_true',
                        help='Clear device list (preserves API key, auth, settings)')
    parser.add_argument('--reset-stats', action='store_true',
                        help='Clear lifetime and session statistics')
    parser.add_argument('--reset-all', action='store_true',
                        help='Factory reset (devices + stats + auth)')
    args = parser.parse_args()

    if args.reset_all:
        cli_reset_all()
        sys.exit(0)
    if args.reset_devices:
        cli_reset_devices()
        sys.exit(0)
    if args.reset_stats:
        cli_reset_stats()
        sys.exit(0)

    ensure_config_dir()
    load_stats()
    load_block_finder_history()  #  Load block attribution history
    load_miner_groups()          #  Load fleet management groups
    load_historical_data()       #  Load historical analytics data
    load_activity_feed()         # Load activity feed
    load_share_audit_log()       # V1.0: Load share audit log
    load_avalon_schedules()      # V1.0: Load Avalon power schedules

    # Start Avalon schedule worker if any schedules are enabled
    if any(s.get("enabled") for s in avalon_schedules.values()):
        start_schedule_worker()

    # Pre-warm network detection for scanner (avoids first-scan delay)
    try:
        subnet, local_ip = get_local_subnet()
        print(f"[STARTUP] Network detected: {subnet} (local IP: {local_ip})")
    except Exception as e:
        print(f"[STARTUP] Network detection failed: {e}")

    # Initialize lifetime stats if needed
    if not lifetime_stats.get("uptime_start"):
        lifetime_stats["uptime_start"] = time.time()
        save_stats()

    print("""
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
                 SPIRAL DASHBOARD - Mining Pool Web Interface
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
    """)

    print("Starting Spiral Pool Dashboard on http://localhost:1618")
    print(f"Pool API: {POOL_API_URL}")
    print(f"Prometheus Metrics: {PROMETHEUS_URL}")
    print("Press CTRL+C to stop the server")

    # BC2 (Bitcoin II) Security Warning - Alert operators about address format risk
    load_multi_coin_config()
    if MULTI_COIN_NODES.get("BC2", {}).get("enabled", False):
        print("\n" + "=" * 78)
        print("⚠️  BITCOIN II (BC2) SECURITY WARNING ⚠️")
        print("=" * 78)
        print("BC2 is ENABLED in your configuration.")
        print("")
        print("CRITICAL: BC2 uses IDENTICAL address formats to Bitcoin (bc1q, 1, 3).")
        print("You CANNOT distinguish a BC2 address from a BTC address by looking at it.")
        print("")
        print("VERIFY YOUR CONFIGURATION:")
        print("  1. Your BC2 address was generated using Bitcoin II Core (NOT Bitcoin Core)")
        print("  2. Your BC2 RPC is pointing to a Bitcoin II node (port 8339), not Bitcoin")
        print("  3. Consider extended confirmation times (200+ blocks) for BC2 due to")
        print("     lower network hashrate and higher reorg risk")
        print("")
        print("Standard confirmation: 100 blocks (coinbase maturity) = ~16.7 hours")
        print("Recommended for BC2:  200 blocks (conservative)      = ~33.3 hours")
        print("=" * 78 + "\n")

    # R-20 FIX: Register signal handlers for clean shutdown.
    # Saves stats and flushes state before exit.
    def _dashboard_shutdown(signum, frame):
        sig_name = signal.Signals(signum).name if hasattr(signal, 'Signals') else str(signum)
        print(f"\n[SHUTDOWN] Received {sig_name}, saving state...")
        try:
            save_stats()
        except Exception as e:
            print(f"[SHUTDOWN] Error saving stats: {e}")
        try:
            save_historical_data()
        except Exception as e:
            print(f"[SHUTDOWN] Error saving historical data: {e}")
        try:
            save_activity_feed()
        except Exception as e:
            print(f"[SHUTDOWN] Error saving activity feed: {e}")
        print("[SHUTDOWN] State saved, exiting.")
        sys.exit(0)

    signal.signal(signal.SIGTERM, _dashboard_shutdown)
    signal.signal(signal.SIGINT, _dashboard_shutdown)

    # R-10 FIX: Wait for Pool API to be available before starting.
    # After cold boot, stratum may take minutes to start. Without this check,
    # dashboard serves empty data for 60+ seconds until background thread fetches.
    print("[STARTUP] Waiting for Pool API...")
    for attempt in range(1, 37):  # Up to 3 minutes (36 x 5s)
        try:
            resp = requests.get(f"{POOL_API_URL}/api/pools", timeout=3)
            if resp.status_code == 200:
                print(f"[STARTUP] Pool API ready (attempt {attempt})")
                break
        except Exception:
            pass
        if attempt % 6 == 0:
            print(f"[STARTUP] Still waiting for Pool API... ({attempt * 5}s)")
        time.sleep(5)
    else:
        print("[STARTUP] Pool API not available after 180s — starting anyway (will retry in background)")

    # Start background data collection thread
    collector_thread = threading.Thread(target=background_data_collection, daemon=True)
    collector_thread.start()
    print("Background data collection started (historical data, alerts, watchdog)")
    print("WebSocket real-time updates enabled")

    # Production mode - use socketio for WebSocket support
    # NOTE: For production deployments, use gunicorn instead of the development server.
    # See run.sh for the production startup command
    socketio.run(app, host='0.0.0.0', port=1618, debug=False)
