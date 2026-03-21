# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Spiral Pool - Windows Installer v1.1

.DESCRIPTION
    Fully automated installation of Spiral Pool using Docker Desktop for Windows.
    Supports both Hyper-V and WSL2 backends with automatic detection and configuration.

    This installer:
    - Detects Windows edition and available virtualization backends
    - Installs WSL2 or enables Hyper-V as needed
    - Downloads and installs Docker Desktop automatically
    - Configures networking, firewall, and port forwarding
    - Sets up auto-start and health monitoring

.NOTES
    Version: 1.1.1
    Author: Spiral Pool Contributors
    Status: EXPERIMENTAL

    ════════════════════════════════════════════════════════════════════
                        ⚠️  EXPERIMENTAL STATUS  ⚠️
    ════════════════════════════════════════════════════════════════════
      This installer has not been extensively tested and may have issues.

      This installer has NOT been validated for production use.
      The following scenarios are UNTESTED:
        • Blockchain sync completion on Docker Desktop
        • 24/7 mining operation stability
        • Volume persistence across Docker Desktop updates
        • Performance under high miner connection load

      For production mining: Use Linux (Ubuntu 24.04 LTS)
    ════════════════════════════════════════════════════════════════════

.EXAMPLE
    # Interactive installation
    .\install-windows.ps1

    # Fully automated installation
    .\install-windows.ps1 -Unattended -PoolAddress "DYourAddressHere" -Coin DGB

    # Show help
    .\install-windows.ps1 -Help
#>

param(
    [switch]$Unattended,
    [string]$PoolAddress,
    [ValidateSet("DGB", "BTC", "BCH", "BC2", "LTC", "DOGE", "DGB-SCRYPT", "PEP", "CAT", "NMC", "SYS", "XMY", "FBTC", "QBX")]
    [string]$Coin,  # Required in unattended mode; interactive mode shows menu
    [string]$DataDrive = "C:",
    [switch]$AcceptTerms,
    [switch]$Help
)

# ═══════════════════════════════════════════════════════════════════════════════
# CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════

$ErrorActionPreference = "Stop"
$Script:InstallDir = "$DataDrive\SpiralPool"
$Script:Version = "1.1.1"
$Script:LogFile = "$env:TEMP\spiralpool-install.log"

# ═══════════════════════════════════════════════════════════════════════════════
# COIN CONFIGURATION TABLE (single source of truth for all 14 coins)
# ═══════════════════════════════════════════════════════════════════════════════

$Script:CoinConfig = @{
    DGB          = @{ Container="digibyte";       RpcPort=14022; P2pPort=12024; ZmqPort=28532; RpcUser="spiraldgb";  StratumPort=3333;  TlsPort=3335;  PoolCoin="digibyte";        Profile="dgb";        Algo="SHA256d"; Storage="60 GB";   CliName="digibyte-cli" }
    BTC          = @{ Container="bitcoin";        RpcPort=8332;  P2pPort=8333;  ZmqPort=28332; RpcUser="spiralbtc";  StratumPort=4333;  TlsPort=4335;  PoolCoin="bitcoin";         Profile="btc";        Algo="SHA256d"; Storage="600 GB";  CliName="bitcoin-cli" }
    BCH          = @{ Container="bitcoincash";    RpcPort=8432;  P2pPort=8433;  ZmqPort=28432; RpcUser="spiralbch";  StratumPort=5333;  TlsPort=5335;  PoolCoin="bitcoincash";     Profile="bch";        Algo="SHA256d"; Storage="250 GB";  CliName="bitcoin-cli" }
    BC2          = @{ Container="bitcoinii";      RpcPort=8339;  P2pPort=8338;  ZmqPort=28338; RpcUser="spiralbc2";  StratumPort=6333;  TlsPort=6335;  PoolCoin="bitcoinii";       Profile="bc2";        Algo="SHA256d"; Storage="10 GB";   CliName="bitcoinii-cli" }
    NMC          = @{ Container="namecoin";       RpcPort=8336;  P2pPort=8334;  ZmqPort=28336; RpcUser="spiralnmc";  StratumPort=14335; TlsPort=14337; PoolCoin="namecoin";        Profile="nmc";        Algo="SHA256d"; Storage="15 GB";   CliName="namecoin-cli" }
    SYS          = @{ Container="syscoin";        RpcPort=8370;  P2pPort=8369;  ZmqPort=28370; RpcUser="spiralsys";  StratumPort=15335; TlsPort=15337; PoolCoin="syscoin";         Profile="sys";        Algo="SHA256d"; Storage="25 GB";   CliName="syscoin-cli" }
    XMY          = @{ Container="myriadcoin";     RpcPort=10889; P2pPort=10888; ZmqPort=28889; RpcUser="spiralxmy";  StratumPort=17335; TlsPort=17337; PoolCoin="myriadcoin";      Profile="xmy";        Algo="SHA256d"; Storage="8 GB";    CliName="myriadcoin-cli" }
    FBTC         = @{ Container="fractalbitcoin"; RpcPort=8340;  P2pPort=8341;  ZmqPort=28340; RpcUser="spiralfbtc"; StratumPort=18335; TlsPort=18337; PoolCoin="fractalbitcoin";  Profile="fbtc";       Algo="SHA256d"; Storage="10 GB";   CliName="bitcoin-cli" }
    QBX          = @{ Container="qbitx";          RpcPort=8344;  P2pPort=8345;  ZmqPort=28344; RpcUser="spiralqbx";  StratumPort=20335; TlsPort=20337; PoolCoin="qbitx";            Profile="qbx";        Algo="SHA256d"; Storage="5 GB";    CliName="qbitx-cli" }
    LTC          = @{ Container="litecoin";       RpcPort=9332;  P2pPort=9333;  ZmqPort=28933; RpcUser="spiralltc";  StratumPort=7333;  TlsPort=7335;  PoolCoin="litecoin";        Profile="ltc";        Algo="Scrypt";  Storage="150 GB";  CliName="litecoin-cli" }
    DOGE         = @{ Container="dogecoin";       RpcPort=22555; P2pPort=22556; ZmqPort=28555; RpcUser="spiraldoge"; StratumPort=8335;  TlsPort=8342;  PoolCoin="dogecoin";        Profile="doge";       Algo="Scrypt";  Storage="80 GB";   CliName="dogecoin-cli" }
    "DGB-SCRYPT" = @{ Container="digibyte";       RpcPort=14022; P2pPort=12024; ZmqPort=28532; RpcUser="spiraldgb";  StratumPort=3336;  TlsPort=3338;  PoolCoin="digibyte-scrypt"; Profile="dgb-scrypt"; Algo="Scrypt";  Storage="60 GB";   CliName="digibyte-cli" }
    PEP          = @{ Container="pepecoin";       RpcPort=33873; P2pPort=33874; ZmqPort=28873; RpcUser="spiralpep";  StratumPort=10335; TlsPort=10337; PoolCoin="pepecoin";        Profile="pep";        Algo="Scrypt";  Storage="5 GB";    CliName="pepecoin-cli" }
    CAT          = @{ Container="catcoin";        RpcPort=9932;  P2pPort=9933;  ZmqPort=28932; RpcUser="spiralcat";  StratumPort=12335; TlsPort=12337; PoolCoin="catcoin";         Profile="cat";        Algo="Scrypt";  Storage="5 GB";    CliName="catcoin-cli" }
}

# Wallet address validation patterns per coin
$Script:WalletPatterns = @{
    DGB          = "^(D[a-km-zA-HJ-NP-Z1-9]{25,34}|S[a-km-zA-HJ-NP-Z1-9]{25,34}|dgb1[a-z0-9]{38,59})$"
    BTC          = "^(1[a-km-zA-HJ-NP-Z1-9]{25,34}|3[a-km-zA-HJ-NP-Z1-9]{25,34}|bc1q[a-z0-9]{38,58})$"
    BCH          = "^(bitcoincash:[qp][a-z0-9]{41}|[13][a-km-zA-HJ-NP-Z1-9]{25,34})$"
    BC2          = "^(1[a-km-zA-HJ-NP-Z1-9]{25,34}|3[a-km-zA-HJ-NP-Z1-9]{25,34}|bc1q[a-z0-9]{38,58})$"
    NMC          = "^(N[a-km-zA-HJ-NP-Z1-9]{25,34}|M[a-km-zA-HJ-NP-Z1-9]{25,34})$"
    SYS          = "^(sys1[a-z0-9]{38,59})$"
    XMY          = "^(M[a-km-zA-HJ-NP-Z1-9]{25,34})$"
    FBTC         = "^(1[a-km-zA-HJ-NP-Z1-9]{25,34}|3[a-km-zA-HJ-NP-Z1-9]{25,34}|bc1q[a-z0-9]{38,58})$"
    QBX          = "^((1|3)[a-km-zA-HJ-NP-Z1-9]{25,34}|pq[a-zA-Z0-9]{20,80})$"
    LTC          = "^(L[a-km-zA-HJ-NP-Z1-9]{25,34}|M[a-km-zA-HJ-NP-Z1-9]{25,34}|ltc1[a-z0-9]{38,59})$"
    DOGE         = "^(D[a-km-zA-HJ-NP-Z1-9]{25,34}|A[a-km-zA-HJ-NP-Z1-9]{25,34})$"
    "DGB-SCRYPT" = "^(D[a-km-zA-HJ-NP-Z1-9]{25,34}|S[a-km-zA-HJ-NP-Z1-9]{25,34}|dgb1[a-z0-9]{38,59})$"
    PEP          = "^(P[a-km-zA-HJ-NP-Z1-9]{25,34})$"
    CAT          = "^(C[a-km-zA-HJ-NP-Z1-9]{25,34}|9[a-km-zA-HJ-NP-Z1-9]{25,34})$"
}

# ═══════════════════════════════════════════════════════════════════════════════
# LOGGING & OUTPUT
# ═══════════════════════════════════════════════════════════════════════════════

function Write-Log {
    param([string]$Message, [string]$Level = "INFO")
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    "$timestamp [$Level] $Message" | Out-File -Append -FilePath $Script:LogFile -Encoding UTF8

    switch ($Level) {
        "ERROR" { Write-Host "  [X] $Message" -ForegroundColor Red }
        "WARN"  { Write-Host "  [!] $Message" -ForegroundColor Yellow }
        "OK"    { Write-Host "  [OK] $Message" -ForegroundColor Green }
        "STEP"  { Write-Host "  [*] $Message" -ForegroundColor Cyan }
        default { Write-Host "      $Message" -ForegroundColor White }
    }
}

function Write-Banner {
    Clear-Host
    Write-Host ""
    Write-Host "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "                          SPIRAL POOL" -ForegroundColor White
    Write-Host "                       WINDOWS INSTALLER" -ForegroundColor Green
    Write-Host "                           v1.1.1" -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "           Solo Mining Pool - SHA256d & Scrypt (14 Coins)" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Cyan
    Write-Host ""
}

function Show-LegalAcceptance {
    # Skip if -AcceptTerms was passed
    if ($AcceptTerms) {
        Write-Log "Terms accepted via -AcceptTerms flag" "OK"
        return
    }

    Write-Host ""
    Write-Host "    .-----------------------------------------------------------------------." -ForegroundColor Yellow
    Write-Host "    |                 " -ForegroundColor Yellow -NoNewline
    Write-Host "TERMS OF USE & LEGAL NOTICES" -ForegroundColor White -NoNewline
    Write-Host "                          |" -ForegroundColor Yellow
    Write-Host "    |-----------------------------------------------------------------------|" -ForegroundColor Yellow
    Write-Host "    |                                                                       |" -ForegroundColor Yellow
    Write-Host "    |   By proceeding, you acknowledge and agree to the following:          |" -ForegroundColor Yellow
    Write-Host "    |                                                                       |" -ForegroundColor Yellow
    Write-Host "    |   " -ForegroundColor Yellow -NoNewline
    Write-Host "NO WARRANTY:" -ForegroundColor White -NoNewline
    Write-Host " This software is provided ""AS IS"" without warranty   |" -ForegroundColor Yellow
    Write-Host "    |   of any kind. See LICENSE and TERMS.md for full details.             |" -ForegroundColor Yellow
    Write-Host "    |                                                                       |" -ForegroundColor Yellow
    Write-Host "    |   " -ForegroundColor Yellow -NoNewline
    Write-Host "LIMITATION OF LIABILITY:" -ForegroundColor White -NoNewline
    Write-Host " The authors are not liable for any       |" -ForegroundColor Yellow
    Write-Host "    |   damages, including loss of cryptocurrency, data, or profits.        |" -ForegroundColor Yellow
    Write-Host "    |                                                                       |" -ForegroundColor Yellow
    Write-Host "    |   " -ForegroundColor Yellow -NoNewline
    Write-Host "YOUR RESPONSIBILITIES:" -ForegroundColor White -NoNewline
    Write-Host " You are solely responsible for:            |" -ForegroundColor Yellow
    Write-Host "    |     - Compliance with all applicable laws and regulations              |" -ForegroundColor Yellow
    Write-Host "    |     - Money transmission / MSB registration requirements              |" -ForegroundColor Yellow
    Write-Host "    |     - Securing your systems, wallets, and infrastructure              |" -ForegroundColor Yellow
    Write-Host "    |     - Backing up all data and disaster recovery                       |" -ForegroundColor Yellow
    Write-Host "    |     - Tax obligations arising from cryptocurrency mining              |" -ForegroundColor Yellow
    Write-Host "    |                                                                       |" -ForegroundColor Yellow
    Write-Host "    |   " -ForegroundColor Yellow -NoNewline
    Write-Host "DATA HANDLING:" -ForegroundColor White -NoNewline
    Write-Host " You are the data controller. See PRIVACY.md.       |" -ForegroundColor Yellow
    Write-Host "    |                                                                       |" -ForegroundColor Yellow
    Write-Host "    |   " -ForegroundColor Yellow -NoNewline
    Write-Host "SPECIFIC HAZARDS:" -ForegroundColor Red -NoNewline
    Write-Host " Financial loss, security breaches, legal        |" -ForegroundColor Yellow
    Write-Host "    |   risk, data loss, and regulatory penalties. See WARNINGS.md.         |" -ForegroundColor Yellow
    Write-Host "    |                                                                       |" -ForegroundColor Yellow
    Write-Host "    |   Full documents: TERMS.md, LICENSE, PRIVACY.md, WARNINGS.md          |" -ForegroundColor Yellow
    Write-Host "    |                                                                       |" -ForegroundColor Yellow
    Write-Host "    '-----------------------------------------------------------------------'" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  To accept these terms and continue, type: " -ForegroundColor White -NoNewline
    Write-Host "I AGREE" -ForegroundColor Green
    Write-Host "  To cancel installation, press Ctrl+C or type anything else." -ForegroundColor White
    Write-Host ""

    $response = Read-Host "  Your response"

    if ($response -ne "I AGREE" -and $response -ne "i agree" -and $response -ne "I agree") {
        Write-Host ""
        Write-Log "Terms not accepted. Installation cancelled." "ERROR"
        exit 1
    }

    Write-Host ""
    Write-Log "Terms accepted. Proceeding with installation." "OK"
    Write-Host ""
}

function Show-Help {
    Write-Host @"

  USAGE:
    .\install-windows.ps1 [options]

  OPTIONS:
    -Unattended       Fully automated installation (no prompts)
    -PoolAddress      Your wallet address for mining rewards
    -Coin             Coin to mine: BC2, BCH, BTC, CAT, DGB, DGB-SCRYPT, DOGE, FBTC, LTC, NMC, PEP, QBX, SYS, or XMY
    -DataDrive        Drive for blockchain data (default: C:)
    -AcceptTerms      Accept Terms of Use and warnings (non-interactive)
    -Help             Show this help message

  EXAMPLES:
    # Interactive installation (recommended)
    .\install-windows.ps1

    # Automated DigiByte solo mining
    .\install-windows.ps1 -Unattended -PoolAddress "DYourAddressHere" -Coin DGB

    # Automated Bitcoin solo mining
    .\install-windows.ps1 -Unattended -PoolAddress "bc1..." -Coin BTC

    # Automated Litecoin solo mining on D: drive
    .\install-windows.ps1 -Unattended -PoolAddress "ltc1..." -Coin LTC -DataDrive "D:"

  INTERACTIVE MODE:
    When run without -Unattended, the installer presents a coin selection menu:

    SHA256d: DGB (~60GB), BTC (~600GB), BCH (~250GB), BC2 (~10GB)
             NMC (~15GB), SYS (~25GB), XMY (~8GB), FBTC (~10GB), QBX (~5GB)
    Scrypt:  LTC (~150GB), DOGE (~80GB), DGB-SCRYPT (~60GB), PEP (~5GB), CAT (~5GB)

    You will be prompted for:
    - Wallet address for the selected coin
    - Blockchain storage path
    - Coinbase text (message in blocks)
    - Network configuration

  REQUIREMENTS:
    - Windows 11 (Home, Pro, Enterprise, or Education)
    - 8 GB RAM minimum (16 GB recommended)
    - Storage varies by coin (see menu)
    - Administrator privileges

  LIMITATIONS (Docker deployment — single-coin V1 only):
    - No multi-coin simultaneous mining
    - No merge mining (BTC+NMC, LTC+DOGE, etc.)
    - No V2 Enhanced Stratum (encrypted binary protocol)
    - No High Availability (HA failover)
    For full features: sudo ./install.sh on Ubuntu 24.04 LTS

  DOCKER BACKEND:
    The installer automatically configures the best available backend:
    - WSL2 (preferred): Works on all Windows 11 editions
    - Hyper-V: Available on Pro/Enterprise/Education

  NETWORKING:
    - Ports are published to 0.0.0.0 (all interfaces)
    - Windows Firewall rules created automatically
    - WSL2 port forwarding configured if needed
    - Miners connect to your Windows IP address

  WHAT GETS INSTALLED:
    - WSL2 or Hyper-V (if not already enabled)
    - Docker Desktop (if not already installed)
    - Spiral Pool containers (stratum, dashboard, database, blockchain node)
    - Windows Firewall rules
    - Scheduled tasks for auto-start and health monitoring

  PERSISTENCE:
    - Containers auto-restart on failure (Docker restart policy)
    - Health check runs every 5 minutes (scheduled task)
    - Auto-start on Windows boot (scheduled task)
    - WSL2 port forwarding updates on boot

"@
}

# ═══════════════════════════════════════════════════════════════════════════════
# SYSTEM DETECTION
# ═══════════════════════════════════════════════════════════════════════════════

function Get-WindowsEdition {
    $edition = (Get-CimInstance -ClassName Win32_OperatingSystem).Caption
    $build = [System.Environment]::OSVersion.Version.Build

    $supportsHyperV = $edition -match "Pro|Enterprise|Education"
    $supportsWSL2 = $build -ge 22000  # Windows 11+

    return @{
        Edition = $edition
        Build = $build
        SupportsHyperV = $supportsHyperV
        SupportsWSL2 = $supportsWSL2
    }
}

function Test-HyperVEnabled {
    try {
        $hyperv = Get-WindowsOptionalFeature -FeatureName Microsoft-Hyper-V -Online -ErrorAction SilentlyContinue
        return ($hyperv.State -eq "Enabled")
    } catch {
        return $false
    }
}

function Test-WSL2Installed {
    try {
        $wslOutput = wsl --status 2>&1
        if ($LASTEXITCODE -eq 0) {
            return $true
        }
        # Also check if wsl command exists and has distributions
        $wslList = wsl -l -v 2>&1
        return ($LASTEXITCODE -eq 0)
    } catch {
        return $false
    }
}

function Get-DockerBackend {
    $winInfo = Get-WindowsEdition

    # Check what's already available
    $hyperVEnabled = Test-HyperVEnabled
    $wsl2Installed = Test-WSL2Installed

    Write-Log "Windows: $($winInfo.Edition) (Build $($winInfo.Build))" "INFO"
    Write-Log "Hyper-V capable: $($winInfo.SupportsHyperV), WSL2 capable: $($winInfo.SupportsWSL2)" "INFO"
    Write-Log "Hyper-V enabled: $hyperVEnabled, WSL2 installed: $wsl2Installed" "INFO"

    # Determine best backend - prefer WSL2 as it works on all editions
    if ($wsl2Installed) {
        return @{ Backend = "WSL2"; Ready = $true; NeedsReboot = $false }
    }
    elseif ($hyperVEnabled) {
        return @{ Backend = "HyperV"; Ready = $true; NeedsReboot = $false }
    }
    elseif ($winInfo.SupportsWSL2) {
        # WSL2 is preferred and works on all editions including Home
        return @{ Backend = "WSL2"; Ready = $false; NeedsReboot = $true }
    }
    elseif ($winInfo.SupportsHyperV) {
        return @{ Backend = "HyperV"; Ready = $false; NeedsReboot = $true }
    }
    else {
        Write-Log "This Windows version is not supported" "ERROR"
        Write-Log "Requires Windows 11 (build 22000+)" "ERROR"
        Write-Log "Windows 10 reached end of support in October 2025" "INFO"
        exit 1
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# INSTALLATION OPTIONS MENU
# ═══════════════════════════════════════════════════════════════════════════════

function Show-DockerLimitations {
    Write-Host ""
    Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Cyan
    Write-Host "  DOCKER DEPLOYMENT - SOLO MINING" -ForegroundColor Cyan
    Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "  This installer sets up a single-coin solo mining pool" -ForegroundColor White
    Write-Host "  using Docker. Everything is included:" -ForegroundColor White
    Write-Host ""
    Write-Host "    [OK] Blockchain node       [OK] Stratum server (V1 + TLS)" -ForegroundColor Green
    Write-Host "    [OK] PostgreSQL database   [OK] Dashboard + Monitoring" -ForegroundColor Green
    Write-Host "    [OK] Spiral Sentinel        [OK] Auto-restart on boot" -ForegroundColor Green
    Write-Host ""
    Write-Host "  Limitations (requires native Linux install):" -ForegroundColor White
    Write-Host "    [X] High Availability (HA failover)" -ForegroundColor Red
    Write-Host "    [X] Multi-coin simultaneous mining" -ForegroundColor Red
    Write-Host "    [X] Merge mining (BTC+NMC, LTC+DOGE, etc.)" -ForegroundColor Red
    Write-Host "    [X] V2 Enhanced Stratum (encrypted binary protocol)" -ForegroundColor Red
    Write-Host ""
    Write-Host "  For full features: sudo ./install.sh on Ubuntu 24.04 LTS" -ForegroundColor DarkGray
    Write-Host ""
}

function Show-SoloCoinMenu {
    Write-Host ""
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    SELECT COIN FOR SOLO MINING" -ForegroundColor White
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""
    Write-Host "  SHA256d Coins:" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "   [1] " -NoNewline -ForegroundColor Cyan
    Write-Host "DGB  - DigiByte" -NoNewline -ForegroundColor Green
    Write-Host "        ~60 GB   Port 3333   (Recommended)" -ForegroundColor DarkGray
    Write-Host "   [2] " -NoNewline -ForegroundColor Cyan
    Write-Host "BTC  - Bitcoin" -NoNewline -ForegroundColor Yellow
    Write-Host "         ~600 GB  Port 4333" -ForegroundColor DarkGray
    Write-Host "   [3] " -NoNewline -ForegroundColor Cyan
    Write-Host "BCH  - Bitcoin Cash" -NoNewline -ForegroundColor White
    Write-Host "    ~250 GB  Port 5333" -ForegroundColor DarkGray
    Write-Host "   [4] " -NoNewline -ForegroundColor Cyan
    Write-Host "BC2  - Bitcoin II" -NoNewline -ForegroundColor White
    Write-Host "      ~10 GB   Port 6333" -ForegroundColor DarkGray
    Write-Host "   [5] " -NoNewline -ForegroundColor Cyan
    Write-Host "NMC  - Namecoin" -NoNewline -ForegroundColor White
    Write-Host "       ~15 GB   Port 14335" -ForegroundColor DarkGray
    Write-Host "   [6] " -NoNewline -ForegroundColor DarkGray
    Write-Host "SYS  - Syscoin" -NoNewline -ForegroundColor DarkGray
    Write-Host "        (merge-mining only, requires BTC)" -ForegroundColor DarkRed
    Write-Host "   [7] " -NoNewline -ForegroundColor Cyan
    Write-Host "XMY  - Myriadcoin" -NoNewline -ForegroundColor White
    Write-Host "     ~8 GB    Port 17335" -ForegroundColor DarkGray
    Write-Host "   [8] " -NoNewline -ForegroundColor Cyan
    Write-Host "FBTC - Fractal Bitcoin" -NoNewline -ForegroundColor White
    Write-Host " ~10 GB   Port 18335" -ForegroundColor DarkGray
    Write-Host "   [9] " -NoNewline -ForegroundColor Cyan
    Write-Host "QBX  - Q-BitX" -NoNewline -ForegroundColor White
    Write-Host "         ~5 GB    Port 20335" -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "  Scrypt Coins:" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  [10] " -NoNewline -ForegroundColor Cyan
    Write-Host "LTC  - Litecoin" -NoNewline -ForegroundColor Green
    Write-Host "       ~150 GB  Port 7333" -ForegroundColor DarkGray
    Write-Host "  [11] " -NoNewline -ForegroundColor Cyan
    Write-Host "DOGE - Dogecoin" -NoNewline -ForegroundColor White
    Write-Host "       ~80 GB   Port 8335" -ForegroundColor DarkGray
    Write-Host "  [12] " -NoNewline -ForegroundColor Cyan
    Write-Host "DGB  - DigiByte (Scrypt)" -NoNewline -ForegroundColor White
    Write-Host " ~60 GB   Port 3336" -ForegroundColor DarkGray
    Write-Host "  [13] " -NoNewline -ForegroundColor Cyan
    Write-Host "PEP  - PepeCoin" -NoNewline -ForegroundColor White
    Write-Host "       ~5 GB    Port 10335" -ForegroundColor DarkGray
    Write-Host "  [14] " -NoNewline -ForegroundColor Cyan
    Write-Host "CAT  - Catcoin" -NoNewline -ForegroundColor White
    Write-Host "        ~5 GB    Port 12335" -ForegroundColor DarkGray
    Write-Host ""

    $choice = Read-Host "  Select coin (1-14) [default: 1]"
    if ([string]::IsNullOrEmpty($choice)) { $choice = "1" }

    $coinMap = @{
        "1"="DGB"; "2"="BTC"; "3"="BCH"; "4"="BC2"; "5"="NMC"; "6"="SYS"; "7"="XMY"; "8"="FBTC"; "9"="QBX"
        "10"="LTC"; "11"="DOGE"; "12"="DGB-SCRYPT"; "13"="PEP"; "14"="CAT"
    }

    if ($coinMap.ContainsKey($choice)) {
        if ($coinMap[$choice] -eq "SYS") {
            Write-Host ""
            Write-Host "  Syscoin (SYS) is a merge-mining-only coin and cannot be mined standalone." -ForegroundColor Red
            Write-Host "  SYS must be merge-mined with BTC using the native Linux installer (install.sh)." -ForegroundColor Yellow
            Write-Host "  Please select a different coin." -ForegroundColor Yellow
            Write-Host ""
            Write-Log "SYS selected but unavailable in Docker single-coin mode" "ERROR"
            exit 1
        }
        return $coinMap[$choice]
    } else {
        Write-Log "Invalid coin selection: $choice" "ERROR"
        exit 1
    }
}

# Show-SelectiveMenu removed — Docker deployment is single-coin only

# ═══════════════════════════════════════════════════════════════════════════════
# RAM VALIDATION
# ═══════════════════════════════════════════════════════════════════════════════

function Test-RAMRequirements {
    Write-Log "Checking RAM requirements..." "STEP"

    try {
        $totalRAM = (Get-CimInstance -ClassName Win32_ComputerSystem).TotalPhysicalMemory
        $totalRAMGB = [math]::Round($totalRAM / 1GB, 1)

        # Docker single-coin: 4GB minimum, 8GB recommended
        $minRAMGB = 4
        $recommendedRAMGB = 8

        Write-Log "Total RAM: $totalRAMGB GB (minimum: $minRAMGB GB, recommended: $recommendedRAMGB GB)" "INFO"

        if ($totalRAMGB -lt $minRAMGB) {
            Write-Log "Insufficient RAM: $totalRAMGB GB available, $minRAMGB GB required" "ERROR"
            Write-Host ""
            Write-Host "  Your system has insufficient RAM for this configuration:" -ForegroundColor Red
            Write-Host "    - Available: $totalRAMGB GB" -ForegroundColor Yellow
            Write-Host "    - Required:  $minRAMGB GB minimum" -ForegroundColor Yellow
            Write-Host ""
            $continue = Read-Host "  Continue anyway? (y/N)"
            if ($continue -ne "y" -and $continue -ne "Y") {
                return $false
            }
            Write-Log "User chose to continue despite insufficient RAM" "WARN"
        } elseif ($totalRAMGB -lt $recommendedRAMGB) {
            Write-Log "RAM is below recommended amount ($totalRAMGB GB < $recommendedRAMGB GB)" "WARN"
        } else {
            Write-Log "RAM requirements met: $totalRAMGB GB" "OK"
        }

        return $true
    } catch {
        Write-Log "Could not check RAM requirements: $_" "WARN"
        return $true  # Continue if check fails
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# PORT CONFLICT DETECTION
# ═══════════════════════════════════════════════════════════════════════════════

function Test-PortAvailability {
    param(
        [hashtable]$Config = @{},
        [string]$SelectedCoin = "DGB"
    )

    Write-Log "Checking for port conflicts..." "STEP"

    $coinInfo = $Script:CoinConfig[$SelectedCoin]

    $portsToCheck = @(
        @{ Port = [int]$coinInfo.StratumPort; Name = "$SelectedCoin Stratum V1" }
        @{ Port = [int]$coinInfo.TlsPort; Name = "$SelectedCoin Stratum TLS" }
        @{ Port = [int]$(if ($Config.ApiPort) { $Config.ApiPort } else { 4000 }); Name = "REST API" }
        @{ Port = [int]$(if ($Config.DashboardPort) { $Config.DashboardPort } else { 1618 }); Name = "Dashboard" }
        @{ Port = [int]$(if ($Config.MetricsPort) { $Config.MetricsPort } else { 9100 }); Name = "Prometheus" }
        @{ Port = 5432; Name = "PostgreSQL" }
        @{ Port = [int]$coinInfo.P2pPort; Name = "$SelectedCoin P2P" }
        @{ Port = [int]$coinInfo.RpcPort; Name = "$SelectedCoin RPC" }
    )

    $conflicts = @()

    foreach ($portInfo in $portsToCheck) {
        try {
            $connection = Get-NetTCPConnection -LocalPort $portInfo.Port -ErrorAction SilentlyContinue
            if ($connection) {
                $process = Get-Process -Id $connection.OwningProcess -ErrorAction SilentlyContinue
                $processName = if ($process) { $process.ProcessName } else { "Unknown" }
                $conflicts += @{
                    Port = $portInfo.Port
                    Name = $portInfo.Name
                    Process = $processName
                }
                Write-Log "Port $($portInfo.Port) ($($portInfo.Name)) is in use by $processName" "WARN"
            }
        } catch {
            # Port is available (no connection found)
        }
    }

    if ($conflicts.Count -gt 0) {
        Write-Host ""
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Yellow
        Write-Host "    PORT CONFLICTS DETECTED" -ForegroundColor White
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Yellow
        Write-Host ""
        Write-Host "  The following ports are already in use:" -ForegroundColor Yellow
        foreach ($conflict in $conflicts) {
            Write-Host "    - Port $($conflict.Port) ($($conflict.Name)): used by $($conflict.Process)" -ForegroundColor Red
        }
        Write-Host ""
        Write-Host "  Options:" -ForegroundColor Cyan
        Write-Host "    1. Stop the conflicting services before installation" -ForegroundColor DarkGray
        Write-Host "    2. Use custom ports during configuration" -ForegroundColor DarkGray
        Write-Host ""
        $continue = Read-Host "  Continue anyway? (y/N)"
        if ($continue -ne "y" -and $continue -ne "Y") {
            return $false
        }
        Write-Log "User chose to continue despite port conflicts" "WARN"
    } else {
        Write-Log "No port conflicts detected" "OK"
    }

    return $true
}

# ═══════════════════════════════════════════════════════════════════════════════
# BLOCKCHAIN STORAGE CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════

function Get-BlockchainStorageLocations {
    param(
        [string]$SelectedCoin = "DGB"
    )

    $coinInfo = $Script:CoinConfig[$SelectedCoin]
    $containerName = $coinInfo.Container
    $storageSize = $coinInfo.Storage

    Write-Host ""
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    BLOCKCHAIN STORAGE CONFIGURATION" -ForegroundColor White
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""
    Write-Host "  $SelectedCoin blockchain data requires ~$storageSize of disk space." -ForegroundColor Yellow
    Write-Host ""

    # Get available drives
    $drives = Get-Volume | Where-Object { $_.DriveLetter -and $_.DriveType -eq 'Fixed' } |
        Select-Object DriveLetter, @{N='FreeGB';E={[math]::Round($_.SizeRemaining/1GB,0)}}, @{N='TotalGB';E={[math]::Round($_.Size/1GB,0)}}

    Write-Host "  Available drives:" -ForegroundColor Cyan
    foreach ($drive in $drives) {
        Write-Host "    $($drive.DriveLetter): - $($drive.FreeGB) GB free / $($drive.TotalGB) GB total" -ForegroundColor DarkGray
    }
    Write-Host ""

    $defaultPath = "$Script:InstallDir\data\$containerName"
    Write-Host "  $SelectedCoin blockchain storage (~$storageSize required)" -ForegroundColor Cyan
    $storagePath = Read-Host "  Path [$defaultPath]"
    if ([string]::IsNullOrEmpty($storagePath)) {
        $storagePath = $defaultPath
    }

    $storageConfig = @{}
    $storageConfig[$SelectedCoin] = $storagePath

    # Validate path
    $driveLetter = $storagePath.Substring(0, 2)
    if (-not (Test-Path $driveLetter)) {
        Write-Log "Drive $driveLetter does not exist" "ERROR"
        exit 1
    }

    # Check free space
    $volume = Get-Volume -DriveLetter $driveLetter.Substring(0,1) -ErrorAction SilentlyContinue
    if ($volume) {
        $freeGB = [math]::Round($volume.SizeRemaining / 1GB, 0)
        # Parse required GB from storage string (e.g. "60 GB" -> 60)
        $requiredGB = [int]($storageSize -replace '[^\d]', '')
        if ($freeGB -lt $requiredGB) {
            Write-Log "$SelectedCoin requires ~$storageSize but $driveLetter only has $freeGB GB free" "WARN"
            $continue = Read-Host "  Continue anyway? (y/N)"
            if ($continue -ne "y" -and $continue -ne "Y") {
                exit 1
            }
        }
    }

    # Create directory if it doesn't exist
    if (-not (Test-Path $storagePath)) {
        New-Item -ItemType Directory -Path $storagePath -Force | Out-Null
        Write-Log "Created $SelectedCoin storage directory: $storagePath" "OK"
    }

    return $storageConfig
}

# ═══════════════════════════════════════════════════════════════════════════════
# MULTI-COIN WALLET CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════

# Get-MultiCoinConfiguration removed — Docker deployment is single-coin only

# ═══════════════════════════════════════════════════════════════════════════════
# WSL2 INSTALLATION
# ═══════════════════════════════════════════════════════════════════════════════

function Install-WSL2 {
    Write-Log "Installing WSL2 (Windows Subsystem for Linux 2)..." "STEP"

    try {
        # Method 1: Try the new wsl --install command (Windows 11)
        Write-Log "Attempting wsl --install..." "INFO"
        $wslInstall = wsl --install --no-distribution 2>&1

        if ($LASTEXITCODE -eq 0) {
            Write-Log "WSL2 installation initiated via wsl --install" "OK"
            return $true
        }

        # Method 2: Manual installation for older Windows versions
        Write-Log "Using manual WSL2 installation method..." "INFO"

        # Enable WSL feature
        Write-Log "Enabling Windows Subsystem for Linux..." "INFO"
        $result = dism.exe /online /enable-feature /featurename:Microsoft-Windows-Subsystem-Linux /all /norestart 2>&1
        if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 3010) {
            Write-Log "Failed to enable WSL: $result" "WARN"
        }

        # Enable Virtual Machine Platform
        Write-Log "Enabling Virtual Machine Platform..." "INFO"
        $result = dism.exe /online /enable-feature /featurename:VirtualMachinePlatform /all /norestart 2>&1
        if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 3010) {
            Write-Log "Failed to enable VMP: $result" "WARN"
        }

        # Download and install WSL2 kernel update
        Write-Log "Downloading WSL2 kernel update..." "INFO"
        $wslUpdateUrl = "https://wslstorestorage.blob.core.windows.net/wslblob/wsl_update_x64.msi"
        $wslUpdatePath = "$env:TEMP\wsl_update_x64.msi"

        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -Uri $wslUpdateUrl -OutFile $wslUpdatePath -UseBasicParsing

        Write-Log "Installing WSL2 kernel update..." "INFO"
        Start-Process msiexec.exe -ArgumentList "/i", "`"$wslUpdatePath`"", "/quiet", "/norestart" -Wait -NoNewWindow

        # Set WSL2 as default version
        wsl --set-default-version 2 2>&1 | Out-Null

        # Cleanup
        Remove-Item $wslUpdatePath -Force -ErrorAction SilentlyContinue

        Write-Log "WSL2 installation complete" "OK"
        return $true

    } catch {
        Write-Log "WSL2 installation failed: $_" "ERROR"
        return $false
    }
}

function Enable-HyperV {
    Write-Log "Enabling Hyper-V..." "STEP"

    try {
        $result = Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V -All -NoRestart -ErrorAction Stop
        Write-Log "Hyper-V enabled successfully" "OK"
        return $true
    } catch {
        Write-Log "Failed to enable Hyper-V: $_" "ERROR"
        return $false
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# DOCKER DESKTOP INSTALLATION
# ═══════════════════════════════════════════════════════════════════════════════

function Test-DockerInstalled {
    try {
        # Check primary installation path
        $dockerPath = "C:\Program Files\Docker\Docker\Docker Desktop.exe"
        if (Test-Path $dockerPath) {
            return $true
        }

        # Check alternative installation paths
        $altPaths = @(
            "$env:ProgramFiles\Docker\Docker\Docker Desktop.exe",
            "$env:LOCALAPPDATA\Docker\Docker Desktop.exe"
        )
        foreach ($path in $altPaths) {
            if (Test-Path $path) {
                return $true
            }
        }

        # Also try running docker command (may be in PATH)
        $null = docker --version 2>&1
        return ($LASTEXITCODE -eq 0)
    } catch {
        return $false
    }
}

function Test-DockerRunning {
    param(
        [int]$RetryCount = 3,
        [int]$RetryDelaySeconds = 5
    )

    for ($i = 1; $i -le $RetryCount; $i++) {
        try {
            $result = docker info 2>&1
            if ($LASTEXITCODE -eq 0) {
                return $true
            }

            # Check if Docker daemon is starting
            if ($result -match "starting" -or $result -match "initializing") {
                if ($i -lt $RetryCount) {
                    Write-Host "  Docker daemon is starting, waiting ${RetryDelaySeconds}s..." -ForegroundColor Yellow
                    Start-Sleep -Seconds $RetryDelaySeconds
                    continue
                }
            }
        } catch {
            if ($i -lt $RetryCount) {
                Start-Sleep -Seconds $RetryDelaySeconds
                continue
            }
        }
    }
    return $false
}

function Install-DockerDesktop {
    param([string]$Backend)

    Write-Log "Docker Desktop not found - installing automatically..." "STEP"

    $dockerUrl = "https://desktop.docker.com/win/main/amd64/Docker%20Desktop%20Installer.exe"
    $installerPath = "$env:TEMP\DockerDesktopInstaller.exe"

    try {
        # Download Docker Desktop
        Write-Log "Downloading Docker Desktop (~500MB, please wait)..." "INFO"
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

        $webClient = New-Object System.Net.WebClient
        $webClient.DownloadFile($dockerUrl, $installerPath)

        if (-not (Test-Path $installerPath)) {
            throw "Download failed - installer not found"
        }

        $fileSize = (Get-Item $installerPath).Length / 1MB
        Write-Log "Download complete ($([math]::Round($fileSize, 0)) MB)" "OK"

        # Install Docker Desktop silently
        Write-Log "Installing Docker Desktop (this may take a few minutes)..." "INFO"

        $installArgs = @("install", "--quiet", "--accept-license")
        if ($Backend -eq "WSL2") {
            $installArgs += "--backend=wsl-2"
        } else {
            $installArgs += "--backend=hyper-v"
        }

        $process = Start-Process -FilePath $installerPath -ArgumentList $installArgs -Wait -PassThru -NoNewWindow

        if ($process.ExitCode -ne 0) {
            Write-Log "Docker installer exited with code $($process.ExitCode)" "WARN"
        }

        # Cleanup installer
        Remove-Item $installerPath -Force -ErrorAction SilentlyContinue

        # Verify installation
        Start-Sleep -Seconds 5
        if (Test-Path "C:\Program Files\Docker\Docker\Docker Desktop.exe") {
            Write-Log "Docker Desktop installed successfully" "OK"
            return $true
        } else {
            Write-Log "Docker Desktop installation may have failed" "WARN"
            return $false
        }

    } catch {
        Write-Log "Failed to install Docker Desktop: $_" "ERROR"
        Write-Log "Please install Docker Desktop manually from: https://www.docker.com/products/docker-desktop" "INFO"
        return $false
    }
}

function Start-DockerDesktop {
    Write-Log "Starting Docker Desktop..." "STEP"

    # Find Docker Desktop executable (check multiple locations)
    $dockerExe = $null
    $dockerPaths = @(
        "C:\Program Files\Docker\Docker\Docker Desktop.exe",
        "$env:ProgramFiles\Docker\Docker\Docker Desktop.exe",
        "$env:LOCALAPPDATA\Docker\Docker Desktop.exe"
    )
    foreach ($path in $dockerPaths) {
        if (Test-Path $path) {
            $dockerExe = $path
            break
        }
    }

    if (-not $dockerExe) {
        Write-Log "Docker Desktop executable not found" "ERROR"
        return $false
    }

    # Start Docker Desktop
    Start-Process $dockerExe -ErrorAction SilentlyContinue

    # Wait for Docker to be ready (up to 3 minutes)
    Write-Log "Waiting for Docker to initialize (this may take up to 3 minutes)..." "INFO"
    $maxAttempts = 90
    $attempt = 0

    while ($attempt -lt $maxAttempts) {
        Start-Sleep -Seconds 2
        $attempt++

        # Use single retry per loop iteration
        if (Test-DockerRunning -RetryCount 1 -RetryDelaySeconds 0) {
            Write-Log "Docker Desktop is running and ready" "OK"
            return $true
        }

        # Show progress every 10 seconds
        if ($attempt % 5 -eq 0) {
            Write-Host "." -NoNewline -ForegroundColor DarkGray
        }
    }

    Write-Host ""
    Write-Log "Docker did not start within expected time" "WARN"
    Write-Log "Please start Docker Desktop manually and run this script again" "INFO"
    return $false
}

# ═══════════════════════════════════════════════════════════════════════════════
# WSL2 NETWORKING (Port Forwarding)
# ═══════════════════════════════════════════════════════════════════════════════

function Configure-WSL2Networking {
    param(
        [string]$SelectedCoin = "DGB"
    )

    Write-Log "Configuring WSL2 networking for Docker Desktop..." "STEP"

    $coinInfo = $Script:CoinConfig[$SelectedCoin]

    try {
        # Docker Desktop with WSL2 backend automatically handles port forwarding
        # when containers use bridge networking with published ports.
        # However, we still configure netsh portproxy as a fallback for edge cases.

        # Check if Docker Desktop is using WSL2 backend
        $dockerInfo = docker info 2>&1
        $isWSL2Backend = $dockerInfo -match "WSL2|wsl2"

        if ($isWSL2Backend) {
            Write-Log "Docker Desktop is using WSL2 backend" "OK"
            Write-Log "Port forwarding is automatic with bridge networking" "INFO"

            # Docker Desktop WSL2 integration handles port publishing automatically
            # when using bridge mode with published ports (our docker-compose.yml)
            # No manual netsh portproxy needed for standard operation

            # However, ensure WSL2 networking is enabled in Docker Desktop settings
            Write-Host ""
            Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Cyan
            Write-Host "    WSL2 NETWORKING CONFIGURATION" -ForegroundColor White
            Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Cyan
            Write-Host ""
            Write-Host "  Docker Desktop WSL2 integration detected." -ForegroundColor Green
            Write-Host "  Ports will be automatically forwarded to 0.0.0.0 (all interfaces)." -ForegroundColor DarkGray
            Write-Host ""
            Write-Host "  If miners on your LAN can't connect, check Docker Desktop settings:" -ForegroundColor Yellow
            Write-Host "    Settings > Resources > WSL Integration > Enable integration" -ForegroundColor DarkGray
            Write-Host "    Settings > General > 'Expose daemon on tcp://localhost:2375'" -ForegroundColor DarkGray
            Write-Host ""

        } else {
            # Hyper-V backend - no special configuration needed
            Write-Log "Docker Desktop is using Hyper-V backend" "OK"
            Write-Log "Port forwarding handled by Docker networking" "INFO"
        }

        # Optional: Set up netsh portproxy as an additional fallback
        # This helps in some network configurations where Docker's port forwarding
        # doesn't properly bind to external interfaces

        $wslIP = $null
        try {
            $wslIP = (wsl hostname -I 2>&1).ToString().Trim().Split(' ')[0]
        } catch {
            # No WSL distro available (Docker manages its own)
        }

        if (-not [string]::IsNullOrEmpty($wslIP) -and $wslIP -notmatch "error") {
            Write-Log "WSL2 VM IP detected: $wslIP" "INFO"

            # Configure port forwarding as backup (coin-specific + utility ports)
            $ports = @(
                $coinInfo.StratumPort,   # Stratum V1
                $coinInfo.TlsPort,       # Stratum TLS
                $coinInfo.RpcPort,       # Daemon RPC
                $coinInfo.P2pPort,       # Daemon P2P
                4000,                    # REST API
                1618,                    # Dashboard
                9100,                    # Prometheus metrics
                5432                     # PostgreSQL
            )

            foreach ($port in $ports) {
                netsh interface portproxy delete v4tov4 listenport=$port listenaddress=0.0.0.0 2>&1 | Out-Null
                netsh interface portproxy add v4tov4 listenport=$port listenaddress=0.0.0.0 connectport=$port connectaddress=$wslIP 2>&1 | Out-Null
            }

            Write-Log "Configured netsh portproxy fallback for $SelectedCoin ports: $($ports -join ', ')" "OK"

            # Create update script for WSL IP changes (runs at boot)
            $portsStr = ($ports | ForEach-Object { $_.ToString() }) -join ", "
            $updateScript = @"
# Spiral Pool WSL2 Port Forwarding Update (Fallback for $SelectedCoin)
# Docker Desktop usually handles this, but this ensures compatibility
`$wslIP = (wsl hostname -I 2>&1).ToString().Trim().Split(' ')[0]
if ([string]::IsNullOrEmpty(`$wslIP) -or `$wslIP -match "error") { exit }

`$ports = @($portsStr)
foreach (`$port in `$ports) {
    netsh interface portproxy delete v4tov4 listenport=`$port listenaddress=0.0.0.0 2>&1 | Out-Null
    netsh interface portproxy add v4tov4 listenport=`$port listenaddress=0.0.0.0 connectport=`$port connectaddress=`$wslIP 2>&1 | Out-Null
}
"@

            $scriptDir = "$Script:InstallDir\scripts"
            New-Item -ItemType Directory -Path $scriptDir -Force -ErrorAction SilentlyContinue | Out-Null

            $scriptPath = "$scriptDir\update-wsl-ports.ps1"
            Set-Content -Path $scriptPath -Value $updateScript -Encoding UTF8

            # Create scheduled task to update ports at startup
            $action = New-ScheduledTaskAction -Execute "powershell.exe" `
                -Argument "-ExecutionPolicy Bypass -WindowStyle Hidden -File `"$scriptPath`""
            $trigger = New-ScheduledTaskTrigger -AtStartup
            $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries

            Unregister-ScheduledTask -TaskName "SpiralPoolWSLPorts" -Confirm:$false -ErrorAction SilentlyContinue
            Register-ScheduledTask -TaskName "SpiralPoolWSLPorts" -Action $action -Trigger $trigger `
                -Settings $settings -User "SYSTEM" -RunLevel Highest `
                -Description "Update Spiral Pool WSL2 port forwarding (fallback)" | Out-Null

            Write-Log "Created startup task for WSL2 port forwarding fallback" "OK"
        }

    } catch {
        Write-Log "WSL2 networking configuration note: Docker Desktop handles port publishing automatically" "INFO"
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# WINDOWS FIREWALL
# ═══════════════════════════════════════════════════════════════════════════════

function Get-CoinPortsFromManifest {
    <#
    .SYNOPSIS
        Parse coin manifest to extract port information for firewall rules.
    #>
    param([string]$ManifestPath)

    if (-not (Test-Path $ManifestPath)) {
        return $null
    }

    $content = Get-Content $ManifestPath -Raw
    $coins = @{}

    # Split by "- symbol:" to get individual coin blocks
    $coinBlocks = $content -split "(?=\s+-\s+symbol:)"

    foreach ($block in $coinBlocks) {
        if ($block -notmatch "symbol:\s*(\w+)") { continue }

        $symbol = $Matches[1]
        $coin = @{
            Symbol = $symbol
            Name = ""
            StratumV1 = 0
            StratumV2 = 0
            StratumTls = 0
            RpcPort = 0
            ZmqPort = 0
        }

        # Parse name
        if ($block -match "name:\s*([^\r\n]+)") {
            $coin.Name = $Matches[1].Trim()
        }

        # Parse network ports
        if ($block -match "rpc_port:\s*(\d+)") {
            $coin.RpcPort = [int]$Matches[1]
        }
        if ($block -match "zmq_port:\s*(\d+)") {
            $coin.ZmqPort = [int]$Matches[1]
        }

        # Parse mining ports
        if ($block -match "stratum_port:\s*(\d+)") {
            $coin.StratumV1 = [int]$Matches[1]
        }
        if ($block -match "stratum_v2_port:\s*(\d+)") {
            $coin.StratumV2 = [int]$Matches[1]
        }
        if ($block -match "stratum_tls_port:\s*(\d+)") {
            $coin.StratumTls = [int]$Matches[1]
        }

        # Set defaults for V2 and TLS if not specified
        if ($coin.StratumV1 -gt 0) {
            if ($coin.StratumV2 -eq 0) { $coin.StratumV2 = $coin.StratumV1 + 1 }
            if ($coin.StratumTls -eq 0) { $coin.StratumTls = $coin.StratumV1 + 2 }
            $coins[$symbol] = $coin
        }
    }

    return $coins
}

function Configure-Firewall {
    <#
    .SYNOPSIS
        Configure Windows Firewall rules based on enabled coins from manifest.

    .DESCRIPTION
        Reads coin port information from coins.manifest.yaml and creates
        firewall rules for all necessary ports: stratum (V1, V2, TLS),
        common ports (API, Dashboard, Metrics), and optionally RPC/ZMQ.
    #>
    param(
        [hashtable]$Config = @{},
        [string[]]$EnabledCoins = @()
    )

    Write-Log "Configuring Windows Firewall..." "STEP"

    try {
        # Remove existing Spiral Pool rules
        Get-NetFirewallRule -DisplayName "Spiral Pool*" -ErrorAction SilentlyContinue |
            Remove-NetFirewallRule -ErrorAction SilentlyContinue

        # Find manifest path
        $manifestPath = Join-Path $Script:InstallDir "config\coins.manifest.yaml"
        if (-not (Test-Path $manifestPath)) {
            # Try current script directory
            $scriptDir = Split-Path -Parent $MyInvocation.ScriptName
            $manifestPath = Join-Path $scriptDir "config\coins.manifest.yaml"
        }
        if (-not (Test-Path $manifestPath)) {
            $manifestPath = ".\config\coins.manifest.yaml"
        }

        # Parse manifest for coin ports
        $coinPorts = $null
        if (Test-Path $manifestPath) {
            $coinPorts = Get-CoinPortsFromManifest -ManifestPath $manifestPath
            Write-Log "Loaded port configuration from manifest ($($coinPorts.Count) coins)" "INFO"
        } else {
            Write-Log "Manifest not found, using fallback port configuration" "WARN"
        }

        # Common ports (always included)
        $apiPort = if ($Config.ApiPort) { [int]$Config.ApiPort } else { 4000 }
        $dashboardPort = if ($Config.DashboardPort) { [int]$Config.DashboardPort } else { 1618 }
        $metricsPort = if ($Config.MetricsPort) { [int]$Config.MetricsPort } else { 9100 }

        $rules = @(
            @{ Name = "REST API"; Port = $apiPort; Desc = "Pool statistics API" }
            @{ Name = "Dashboard"; Port = $dashboardPort; Desc = "Web dashboard" }
            @{ Name = "Metrics"; Port = $metricsPort; Desc = "Prometheus metrics" }
        )

        # Build coin-specific rules from manifest or fallback to Config
        if ($coinPorts -and $EnabledCoins.Count -gt 0) {
            # Use manifest-based port configuration
            $seenPorts = @{}

            foreach ($coinSymbol in $EnabledCoins) {
                $coin = $coinPorts[$coinSymbol]
                if (-not $coin) {
                    Write-Log "Coin $coinSymbol not found in manifest, skipping firewall rules" "WARN"
                    continue
                }

                $sym = $coin.Symbol
                $name = if ($coin.Name) { $coin.Name } else { $sym }

                # Stratum V1
                if ($coin.StratumV1 -gt 0 -and -not $seenPorts.ContainsKey("$($coin.StratumV1)")) {
                    $rules += @{ Name = "$sym Stratum V1"; Port = $coin.StratumV1; Desc = "$name mining (Stratum V1)" }
                    $seenPorts["$($coin.StratumV1)"] = $true
                }

                # Stratum V2
                if ($coin.StratumV2 -gt 0 -and -not $seenPorts.ContainsKey("$($coin.StratumV2)")) {
                    $rules += @{ Name = "$sym Stratum V2"; Port = $coin.StratumV2; Desc = "$name mining (Stratum V2)" }
                    $seenPorts["$($coin.StratumV2)"] = $true
                }

                # Stratum TLS
                if ($coin.StratumTls -gt 0 -and -not $seenPorts.ContainsKey("$($coin.StratumTls)")) {
                    $rules += @{ Name = "$sym Stratum TLS"; Port = $coin.StratumTls; Desc = "$name encrypted mining" }
                    $seenPorts["$($coin.StratumTls)"] = $true
                }

                Write-Log "Added firewall rules for $sym (Stratum: $($coin.StratumV1), $($coin.StratumV2), $($coin.StratumTls))" "INFO"
            }
        } else {
            # Fallback to Config-based ports (legacy behavior)
            $stratumPort = if ($Config.StratumPort) { [int]$Config.StratumPort } else { 3333 }
            $stratumV2Port = if ($Config.StratumV2Port) { [int]$Config.StratumV2Port } else { 3334 }
            $stratumTlsPort = if ($Config.StratumTlsPort) { [int]$Config.StratumTlsPort } else { 3335 }

            $rules += @{ Name = "Stratum V1"; Port = $stratumPort; Desc = "Mining connections (Stratum V1)" }
            $rules += @{ Name = "Stratum V2"; Port = $stratumV2Port; Desc = "Mining connections (Stratum V2 binary)" }
            $rules += @{ Name = "Stratum TLS"; Port = $stratumTlsPort; Desc = "Encrypted mining connections" }
        }

        # Create firewall rules
        foreach ($rule in $rules) {
            # Use Private,Domain profiles only (not Public) for security
            # This prevents exposure on untrusted networks like public WiFi
            New-NetFirewallRule -DisplayName "Spiral Pool - $($rule.Name)" `
                -Direction Inbound -Protocol TCP -LocalPort $rule.Port `
                -Action Allow -Profile Private,Domain -Description $rule.Desc | Out-Null
            Write-Log "Firewall rule: $($rule.Name) on port $($rule.Port) (Private/Domain only)" "OK"
        }

        Write-Host ""
        Write-Host "  [!] Note: Firewall rules apply to Private and Domain networks only." -ForegroundColor Yellow
        Write-Host "      For security, ports are NOT open on Public networks (e.g., public WiFi)." -ForegroundColor DarkGray
        Write-Host "      If you need public network access, manually adjust the firewall rules." -ForegroundColor DarkGray

        Write-Log "Firewall rules created: $($rules.Count) total" "OK"

    } catch {
        Write-Log "Firewall configuration failed: $_" "WARN"
        Write-Log "You may need to manually allow ports in Windows Firewall" "INFO"
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# POOL CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════

function Get-PoolConfiguration {
    param(
        [string]$Address,
        [string]$CoinType,
        [bool]$Interactive
    )

    Write-Host ""
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    POOL CONFIGURATION" -ForegroundColor White
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""

    # Get pool address interactively if not provided
    if ([string]::IsNullOrEmpty($Address) -and $Interactive) {
        Write-Host "  Enter your $CoinType wallet address for mining rewards." -ForegroundColor Cyan
        Write-Host ""
        $pattern = $Script:WalletPatterns[$CoinType]
        if ($pattern) {
            Write-Host "  Address will be validated for $CoinType format." -ForegroundColor DarkGray
        }
        Write-Host ""
        $Address = Read-Host "  $CoinType Pool Address"
    }

    if ([string]::IsNullOrEmpty($Address)) {
        Write-Log "Wallet address is required" "ERROR"
        Write-Log "Use: .\install-windows.ps1 -PoolAddress 'YOUR_ADDRESS' -Coin $CoinType" "INFO"
        exit 1
    }

    # Validate address format using per-coin pattern
    $addressValid = $false
    $pattern = $Script:WalletPatterns[$CoinType]
    if ($pattern -and $Address -match $pattern) {
        Write-Log "$CoinType address format validated" "OK"
        $addressValid = $true
    }
    if (-not $addressValid) {
        Write-Log "Address format may be invalid for $CoinType. Continuing anyway..." "WARN"
    }
    Write-Host ""

    # Coinbase text
    $coinbaseText = "Mined by Spiral Pool"
    if ($Interactive) {
        Write-Host "  Coinbase message (appears in mined blocks, max 40 bytes)" -ForegroundColor DarkGray
        Write-Host "    a-z, 0-9 = 1 byte each    Emojis = 4 bytes each" -ForegroundColor DarkGray
        $input = Read-Host "  Coinbase Text [Mined by Spiral Pool]"
        if (-not [string]::IsNullOrEmpty($input)) {
            # Validate byte length (max 40 bytes UTF-8)
            $byteCount = [System.Text.Encoding]::UTF8.GetByteCount($input)
            if ($byteCount -gt 40) {
                Write-Log "Coinbase text is $byteCount bytes (max 40). Truncating." "WARN"
                # Truncate to 40 bytes
                $bytes = [System.Text.Encoding]::UTF8.GetBytes($input)
                $coinbaseText = [System.Text.Encoding]::UTF8.GetString($bytes, 0, 40)
            } else {
                $coinbaseText = $input
                Write-Log "Coinbase text: '$coinbaseText' ($byteCount bytes)" "OK"
            }
        }
    }

    # Port configuration (fixed per coin from $CoinConfig)
    $coinInfo = $Script:CoinConfig[$CoinType]
    $stratumPort = "$($coinInfo.StratumPort)"
    $stratumTlsPort = "$($coinInfo.TlsPort)"

    # Default service ports
    $apiPort = "4000"
    $dashboardPort = "1618"
    $metricsPort = "9100"

    if ($Interactive) {
        Write-Host ""
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
        Write-Host "    PORT CONFIGURATION ($CoinType)" -ForegroundColor White
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
        Write-Host ""
        Write-Host "  Stratum V1:  $stratumPort  (fixed per coin)" -ForegroundColor Cyan
        Write-Host "  Stratum TLS: $stratumTlsPort  (fixed per coin)" -ForegroundColor Cyan
        Write-Host ""

        # API Port
        Write-Host "  REST API Port (pool statistics)" -ForegroundColor DarkGray
        $input = Read-Host "  API Port [4000]"
        if (-not [string]::IsNullOrEmpty($input)) { $apiPort = $input }

        # Dashboard Port
        Write-Host "  Dashboard Port (web interface)" -ForegroundColor DarkGray
        $input = Read-Host "  Dashboard Port [1618]"
        if (-not [string]::IsNullOrEmpty($input)) { $dashboardPort = $input }

        # Metrics Port
        Write-Host "  Prometheus Metrics Port" -ForegroundColor DarkGray
        $input = Read-Host "  Metrics Port [9100]"
        if (-not [string]::IsNullOrEmpty($input)) { $metricsPort = $input }
    }

    # Detect server IP (excluding virtual interfaces)
    $serverIP = (Get-NetIPAddress -AddressFamily IPv4 |
        Where-Object {
            $_.InterfaceAlias -notmatch "Loopback|vEthernet|WSL|Docker|Hyper-V" -and
            $_.PrefixOrigin -ne "WellKnown" -and
            $_.IPAddress -notmatch "^169\."
        } |
        Select-Object -First 1).IPAddress

    if ([string]::IsNullOrEmpty($serverIP)) {
        $serverIP = "localhost"
        Write-Log "Could not detect server IP, using localhost" "WARN"
    } elseif ($Interactive) {
        Write-Host ""
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
        Write-Host "    NETWORK CONFIGURATION" -ForegroundColor White
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
        Write-Host ""
        Write-Host "  Detected IP: $serverIP" -ForegroundColor Cyan
        $confirmIp = Read-Host "  Use this IP? (Y/n) or enter different IP"
        if (-not [string]::IsNullOrEmpty($confirmIp) -and $confirmIp -ne "Y" -and $confirmIp -ne "y") {
            $serverIP = $confirmIp
        }
    }

    # Generate secure random passwords
    $chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
    $dbPassword = -join (1..32 | ForEach-Object { $chars[(Get-Random -Maximum $chars.Length)] })
    $rpcPassword = -join (1..32 | ForEach-Object { $chars[(Get-Random -Maximum $chars.Length)] })

    return @{
        PoolAddress = $Address
        CoinbaseText = $coinbaseText
        Coin = $CoinType
        ServerIP = $serverIP
        StratumPort = $stratumPort
        StratumTlsPort = $stratumTlsPort
        ApiPort = $apiPort
        DashboardPort = $dashboardPort
        MetricsPort = $metricsPort
        DbPassword = $dbPassword
        RpcPassword = $rpcPassword
        RpcUser = $coinInfo.RpcUser
        RpcHost = $coinInfo.Container
        RpcPort = "$($coinInfo.RpcPort)"
        ZmqHost = $coinInfo.Container
        ZmqPort = "$($coinInfo.ZmqPort)"
        Profile = $coinInfo.Profile
        DataDir = "$Script:InstallDir\data"
    }
}

function New-EnvironmentFile {
    param(
        [hashtable]$Config,
        [hashtable]$StorageConfig = @{}
    )

    Write-Log "Generating .env configuration..." "STEP"

    $coinInfo = $Script:CoinConfig[$Config.Coin]
    $containerName = $coinInfo.Container
    $storagePath = if ($StorageConfig[$Config.Coin]) { $StorageConfig[$Config.Coin] } else { "$Script:InstallDir\data\$containerName" }

    $algoShort = if ($coinInfo.Algo -eq "SHA256d") { "sha256d" } else { "scrypt" }
    $poolId = "$($coinInfo.Profile)_${algoShort}_1"
    $grafanaPassword = -join ((65..90) + (97..122) + (48..57) | Get-Random -Count 24 | ForEach-Object { [char]$_ })
    # Derive env key: DGB-SCRYPT → DGB (shares digibyte container), BC2 → BC2, etc.
    $coinEnvKey = ($Config.Coin.ToUpper() -replace '-SCRYPT', '') -replace '-', '_'

    $envContent = @"
# Spiral Pool v1.1.1 Docker Configuration
# Generated: $(Get-Date -Format "yyyy-MM-dd HH:mm:ss")
# Mode: Single-Coin ($($Config.Coin)) via Docker profile: $($coinInfo.Profile)

# ═══════════════════════════════════════════════════════════════════════════════
# POOL SETTINGS (used by stratum-entrypoint.sh → config.docker.template)
# ═══════════════════════════════════════════════════════════════════════════════
POOL_COIN=$($coinInfo.PoolCoin)
POOL_ID=$poolId
POOL_ADDRESS=$($Config.PoolAddress)
$($coinEnvKey)_POOL_ADDRESS=$($Config.PoolAddress)
COINBASE_TEXT=$($Config.CoinbaseText)
SERVER_IP=$($Config.ServerIP)

# ═══════════════════════════════════════════════════════════════════════════════
# DAEMON CONNECTION: $($Config.Coin) (profile: $($coinInfo.Profile))
# Variable names must match stratum-entrypoint.sh expectations
# ═══════════════════════════════════════════════════════════════════════════════
DAEMON_HOST=$containerName
DAEMON_RPC_PORT=$($coinInfo.RpcPort)
DAEMON_RPC_USER=$($coinInfo.RpcUser)
DAEMON_RPC_PASSWORD=$($Config.RpcPassword)
DAEMON_ZMQ_PORT=$($coinInfo.ZmqPort)

# ═══════════════════════════════════════════════════════════════════════════════
# COIN NODE RPC CREDENTIALS (required by the $($Config.Coin) daemon container)
# docker-compose.yml declares these with :? (hard error if unset)
# ═══════════════════════════════════════════════════════════════════════════════
$($coinEnvKey)_RPC_USER=$($coinInfo.RpcUser)
$($coinEnvKey)_RPC_PASSWORD=$($Config.RpcPassword)

# ═══════════════════════════════════════════════════════════════════════════════
# BLOCKCHAIN DATA DIRECTORY
# Overrides docker-compose named volume: bind-mounts this Windows path instead.
# ═══════════════════════════════════════════════════════════════════════════════
$($coinEnvKey)_DATA_DIR=$storagePath

# ═══════════════════════════════════════════════════════════════════════════════
# STRATUM PORTS
# ═══════════════════════════════════════════════════════════════════════════════
STRATUM_PORT=$($Config.StratumPort)
STRATUM_PORT_TLS=$($Config.StratumTlsPort)

# ═══════════════════════════════════════════════════════════════════════════════
# DATABASE
# ═══════════════════════════════════════════════════════════════════════════════
DB_USER=spiralstratum
DB_PASSWORD=$($Config.DbPassword)
DB_NAME=spiralstratum

# ═══════════════════════════════════════════════════════════════════════════════
# SERVICE PORTS
# ═══════════════════════════════════════════════════════════════════════════════
API_PORT=$($Config.ApiPort)
DASHBOARD_PORT=$($Config.DashboardPort)
METRICS_PORT=$($Config.MetricsPort)

# ═══════════════════════════════════════════════════════════════════════════════
# DASHBOARD AUTHENTICATION (v1.1.1)
# ═══════════════════════════════════════════════════════════════════════════════
DASHBOARD_AUTH_ENABLED=true
DASHBOARD_SESSION_LIFETIME=24
DASHBOARD_API_KEY=
DASHBOARD_CORS_ORIGINS=

# ═══════════════════════════════════════════════════════════════════════════════
# MONITORING
# ═══════════════════════════════════════════════════════════════════════════════
GRAFANA_ADMIN_PASSWORD=$grafanaPassword
SPIRAL_METRICS_TOKEN=
ADMIN_API_KEY=

# ═══════════════════════════════════════════════════════════════════════════════
# NOTIFICATIONS (optional - configure later)
# ═══════════════════════════════════════════════════════════════════════════════
DISCORD_WEBHOOK_URL=
TELEGRAM_BOT_TOKEN=
TELEGRAM_CHAT_ID=

# ═══════════════════════════════════════════════════════════════════════════════
# FLEET SETTINGS
# ═══════════════════════════════════════════════════════════════════════════════
EXPECTED_FLEET_THS=22
"@

    $envPath = Join-Path $Script:InstallDir "docker\.env"
    Set-Content -Path $envPath -Value $envContent -Encoding UTF8

    # Protect .env file permissions
    try {
        $acl = Get-Acl $envPath
        $acl.SetAccessRuleProtection($true, $false)
        $acl.Access | ForEach-Object { $acl.RemoveAccessRule($_) } | Out-Null
        $currentUser = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
        $accessRule = New-Object System.Security.AccessControl.FileSystemAccessRule(
            $currentUser, "FullControl", "None", "None", "Allow"
        )
        $acl.AddAccessRule($accessRule)
        $systemRule = New-Object System.Security.AccessControl.FileSystemAccessRule(
            "SYSTEM", "FullControl", "None", "None", "Allow"
        )
        $acl.AddAccessRule($systemRule)
        Set-Acl -Path $envPath -AclObject $acl
        Write-Log ".env file permissions restricted to current user and SYSTEM" "OK"
    } catch {
        Write-Log "Could not set restrictive permissions on .env file: $_" "WARN"
    }

    Write-Log "Configuration saved to $envPath" "OK"
}

# ═══════════════════════════════════════════════════════════════════════════════
# DOCKER ENVIRONMENT SETUP
# ═══════════════════════════════════════════════════════════════════════════════

function Initialize-DockerEnvironment {
    param([hashtable]$Config)

    Write-Log "Setting up installation directory..." "STEP"

    # Create directory structure
    $dirs = @(
        "$Script:InstallDir",
        "$Script:InstallDir\docker",
        "$Script:InstallDir\docker\config",
        "$Script:InstallDir\data",
        "$Script:InstallDir\logs",
        "$Script:InstallDir\scripts"
    )

    foreach ($dir in $dirs) {
        if (-not (Test-Path $dir)) {
            New-Item -ItemType Directory -Path $dir -Force | Out-Null
        }
    }

    # Copy docker files from source
    $sourceDocker = Join-Path $PSScriptRoot "docker"
    if (Test-Path $sourceDocker) {
        Copy-Item -Path "$sourceDocker\*" -Destination "$Script:InstallDir\docker\" -Recurse -Force
        Write-Log "Docker configuration files copied" "OK"
    } else {
        Write-Log "Docker source directory not found: $sourceDocker" "ERROR"
        exit 1
    }

    # Copy src/ — the Dockerfiles use 'context: ..' and COPY src/stratum, src/dashboard,
    # src/sentinel from the build context (parent of docker/). Without this copy, docker
    # build fails with "COPY failed: file not found in build context".
    $sourceSrc = Join-Path $PSScriptRoot "src"
    if (Test-Path $sourceSrc) {
        if (-not (Test-Path "$Script:InstallDir\src")) {
            New-Item -ItemType Directory -Path "$Script:InstallDir\src" -Force | Out-Null
        }
        Copy-Item -Path "$sourceSrc\*" -Destination "$Script:InstallDir\src\" -Recurse -Force
        Write-Log "Source files copied for Docker build context" "OK"
    } else {
        Write-Log "Source directory not found: $sourceSrc" "ERROR"
        exit 1
    }

    # Note: Environment file is generated separately by New-EnvironmentFile in Main
}

function Start-SpiralPool {
    param(
        [string]$Profile = "dgb"
    )

    Write-Log "Starting Spiral Pool (profile: $Profile)..." "STEP"

    try {
        Set-Location "$Script:InstallDir\docker"

        # Use Windows-specific docker-compose with bridge networking
        $composeFile = "docker-compose.yml"
        Write-Log "Using Windows docker-compose: $composeFile --profile $Profile" "INFO"

        # Pull images
        Write-Log "Pulling Docker images (this may take several minutes)..." "INFO"
        $pullOutput = docker compose -f $composeFile --profile $Profile pull 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Log "Some images may need to build locally" "INFO"
        }

        # Build any custom images
        Write-Log "Building containers..." "INFO"
        docker compose -f $composeFile --profile $Profile build 2>&1 | ForEach-Object {
            if ($_ -match "Step|Successfully|Building|DONE") {
                Write-Host "      $_" -ForegroundColor DarkGray
            }
        }

        # Start containers in detached mode
        Write-Log "Starting services..." "INFO"
        docker compose -f $composeFile --profile $Profile up -d 2>&1 | Out-Null

        # Wait for services to start
        Start-Sleep -Seconds 10

        # Check container status
        $psOutput = docker compose -f $composeFile --profile $Profile ps --format "table {{.Name}}\t{{.Status}}" 2>&1
        Write-Log "Container status:" "INFO"
        $psOutput | ForEach-Object { Write-Host "      $_" -ForegroundColor DarkGray }

        return $true

    } catch {
        Write-Log "Failed to start containers: $_" "ERROR"
        return $false
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# PERSISTENCE (Auto-start & Health Monitoring)
# ═══════════════════════════════════════════════════════════════════════════════

function Configure-Persistence {
    param(
        [string]$Profile = "dgb"
    )

    Write-Log "Configuring auto-start and health monitoring..." "STEP"

    try {
        # Health check script (uses Windows-specific compose file + profile)
        $installDir = $Script:InstallDir
        $healthScript = @"
# Spiral Pool Health Check (profile: $Profile)
`$logFile = "$installDir\logs\health-check.log"
`$composeFile = "docker-compose.yml"
`$profile = "$Profile"
`$timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"

function Log { param([string]`$Msg) "`$timestamp - `$Msg" | Out-File -Append `$logFile }

try {
    Set-Location "$installDir\docker"

    # Check Docker
    `$dockerOK = `$false
    try { docker info 2>&1 | Out-Null; `$dockerOK = (`$LASTEXITCODE -eq 0) } catch {}

    if (-not `$dockerOK) {
        Log "Docker not running, starting..."
        Start-Process "C:\Program Files\Docker\Docker\Docker Desktop.exe"
        Start-Sleep -Seconds 60
    }

    # Check containers (using Windows compose file + profile)
    `$unhealthy = docker compose -f `$composeFile --profile `$profile ps --format json 2>&1 |
        ConvertFrom-Json -ErrorAction SilentlyContinue |
        Where-Object { `$_.State -ne "running" }

    if (`$unhealthy) {
        Log "Restarting unhealthy containers: `$(`$unhealthy.Name -join ', ')"
        docker compose -f `$composeFile --profile `$profile up -d 2>&1 | Out-Null
    }
} catch {
    Log "Error: `$_"
}
"@

        $healthPath = "$Script:InstallDir\scripts\health-check.ps1"
        Set-Content -Path $healthPath -Value $healthScript -Encoding UTF8

        # Startup script (uses Windows-specific compose file + profile)
        $startupScript = @"
# Spiral Pool Startup (profile: $Profile)
`$composeFile = "docker-compose.yml"
`$profile = "$Profile"
Start-Sleep -Seconds 45  # Wait for Docker Desktop

`$dockerOK = `$false
try { docker info 2>&1 | Out-Null; `$dockerOK = (`$LASTEXITCODE -eq 0) } catch {}

if (-not `$dockerOK) {
    Start-Process "C:\Program Files\Docker\Docker\Docker Desktop.exe"
    Start-Sleep -Seconds 90
}

Set-Location "$installDir\docker"
docker compose -f `$composeFile --profile `$profile up -d
"@

        $startupPath = "$Script:InstallDir\scripts\startup.ps1"
        Set-Content -Path $startupPath -Value $startupScript -Encoding UTF8

        # Remove existing tasks
        "SpiralPoolHealthCheck", "SpiralPoolStartup" | ForEach-Object {
            Unregister-ScheduledTask -TaskName $_ -Confirm:$false -ErrorAction SilentlyContinue
        }

        $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable

        # Health check task (every 5 minutes)
        $healthAction = New-ScheduledTaskAction -Execute "powershell.exe" `
            -Argument "-ExecutionPolicy Bypass -WindowStyle Hidden -File `"$healthPath`""
        $healthTrigger = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 5)

        Register-ScheduledTask -TaskName "SpiralPoolHealthCheck" -Action $healthAction `
            -Trigger $healthTrigger -Settings $settings -User "SYSTEM" -RunLevel Highest `
            -Description "Spiral Pool health monitoring" | Out-Null

        # Startup task
        $startupAction = New-ScheduledTaskAction -Execute "powershell.exe" `
            -Argument "-ExecutionPolicy Bypass -WindowStyle Hidden -File `"$startupPath`""
        $startupTrigger = New-ScheduledTaskTrigger -AtStartup

        Register-ScheduledTask -TaskName "SpiralPoolStartup" -Action $startupAction `
            -Trigger $startupTrigger -Settings $settings -User "SYSTEM" -RunLevel Highest `
            -Description "Start Spiral Pool on boot" | Out-Null

        Write-Log "Scheduled tasks created (health check + auto-start)" "OK"

    } catch {
        Write-Log "Persistence configuration failed: $_" "WARN"
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# INSTALLATION SUMMARY
# ═══════════════════════════════════════════════════════════════════════════════

function Show-Summary {
    param(
        [hashtable]$Config
    )

    $coinInfo = $Script:CoinConfig[$Config.Coin]
    $profile = $coinInfo.Profile

    Write-Host ""
    Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Green
    Write-Host "  INSTALLATION COMPLETE!" -ForegroundColor Green
    Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Green
    Write-Host ""
    Write-Host "  Coin:         $($Config.Coin) ($($coinInfo.Algo))" -ForegroundColor White
    Write-Host "  Pool Address: $($Config.PoolAddress)" -ForegroundColor White
    Write-Host "  Profile:      $profile" -ForegroundColor White
    Write-Host ""
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    CONNECTION DETAILS" -ForegroundColor White
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""
    Write-Host "  Dashboard:    http://$($Config.ServerIP):$($Config.DashboardPort)" -ForegroundColor Green
    Write-Host "  Stratum V1:   stratum+tcp://$($Config.ServerIP):$($Config.StratumPort)" -ForegroundColor Green
    Write-Host "  Stratum TLS:  stratum+ssl://$($Config.ServerIP):$($Config.StratumTlsPort)" -ForegroundColor Green
    Write-Host "  API:          http://$($Config.ServerIP):$($Config.ApiPort)" -ForegroundColor Green
    Write-Host ""
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    MINER CONFIGURATION" -ForegroundColor White
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""
    Write-Host "  Pool:     stratum+tcp://$($Config.ServerIP):$($Config.StratumPort)" -ForegroundColor DarkGray
    Write-Host "  Username: $($Config.PoolAddress).worker1" -ForegroundColor DarkGray
    Write-Host "  Password: x" -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Yellow
    Write-Host "    DASHBOARD AUTHENTICATION (v1.1.1)" -ForegroundColor White
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  On first access, you'll be prompted to create an admin password." -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    PERSISTENCE & SELF-HEALING" -ForegroundColor White
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""
    Write-Host "  - Containers auto-restart on failure (Docker restart policy)" -ForegroundColor DarkGray
    Write-Host "  - Health check runs every 5 minutes (scheduled task)" -ForegroundColor DarkGray
    Write-Host "  - Auto-start on Windows boot (scheduled task)" -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    USEFUL COMMANDS" -ForegroundColor White
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""
    Write-Host "  cd $Script:InstallDir\docker" -ForegroundColor DarkGray
    Write-Host "  docker compose -f docker-compose.yml --profile $profile logs -f   # View logs" -ForegroundColor DarkGray
    Write-Host "  docker compose -f docker-compose.yml --profile $profile ps        # Status" -ForegroundColor DarkGray
    Write-Host "  docker compose -f docker-compose.yml --profile $profile restart   # Restart" -ForegroundColor DarkGray
    Write-Host "  docker compose -f docker-compose.yml --profile $profile down      # Stop" -ForegroundColor DarkGray
    Write-Host "  docker compose -f docker-compose.yml --profile $profile up -d     # Start" -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Yellow
    Write-Host "    LIMITATIONS (Docker deployment)" -ForegroundColor White
    Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  This is a single-coin V1 deployment. For full features:" -ForegroundColor DarkGray
    Write-Host "    - Multi-coin, merge mining, V2, HA: use sudo ./install.sh on Linux" -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "  Install log: $Script:LogFile" -ForegroundColor DarkGray
    Write-Host ""
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN
# ═══════════════════════════════════════════════════════════════════════════════

function Main {
    Write-Banner

    if ($Help) {
        Show-Help
        exit 0
    }

    # Show terms acceptance prompt (or skip with -AcceptTerms)
    Show-LegalAcceptance

    # Validate required parameters (in unattended mode)
    if ($Unattended -and [string]::IsNullOrEmpty($Coin)) {
        Write-Host "ERROR: -Coin parameter is required in unattended mode" -ForegroundColor Red
        Write-Host "Specify coin with: -Coin DGB, -Coin BTC, -Coin LTC, etc." -ForegroundColor Yellow
        Write-Host "Run with -Help for more information" -ForegroundColor Yellow
        exit 1
    }

    # Verify admin privileges
    $isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    if (-not $isAdmin) {
        Write-Log "This script requires Administrator privileges" "ERROR"
        Write-Host "  Right-click PowerShell and select 'Run as Administrator'" -ForegroundColor Yellow
        exit 1
    }

    Write-Log "Spiral Pool v$Script:Version Windows Installer" "OK"
    Write-Log "Log file: $Script:LogFile" "INFO"
    Write-Host ""

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 1: Detect and configure virtualization backend
    # ───────────────────────────────────────────────────────────────────────────

    $backend = Get-DockerBackend

    if (-not $backend.Ready) {
        Write-Host ""
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Yellow
        Write-Host "    VIRTUALIZATION SETUP REQUIRED" -ForegroundColor White
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Yellow
        Write-Host ""
        Write-Host "  Docker Desktop requires $($backend.Backend) which is not yet enabled." -ForegroundColor White
        Write-Host ""

        if (-not $Unattended) {
            $response = Read-Host "  Install $($backend.Backend) now? (Y/n)"
            if ($response -eq "n" -or $response -eq "N") {
                Write-Log "Virtualization backend required for Docker" "ERROR"
                exit 1
            }
        }

        # Install the backend
        $success = $false
        if ($backend.Backend -eq "WSL2") {
            $success = Install-WSL2
        } else {
            $success = Enable-HyperV
        }

        if ($success) {
            Write-Host ""
            Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Yellow
            Write-Host "  RESTART REQUIRED" -ForegroundColor Yellow
            Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Yellow
            Write-Host ""
            Write-Host "  $($backend.Backend) has been installed. Please restart your" -ForegroundColor Yellow
            Write-Host "  computer and run this script again to complete installation." -ForegroundColor Yellow
            Write-Host ""

            if (-not $Unattended) {
                Read-Host "  Press Enter to exit"
            }
            exit 0
        } else {
            exit 1
        }
    }

    Write-Log "Virtualization backend ready: $($backend.Backend)" "OK"

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 1.5: Check RAM requirements (preliminary - will re-check after mode selection)
    # ───────────────────────────────────────────────────────────────────────────

    if (-not (Test-RAMRequirements)) {
        Write-Log "RAM requirements not met" "ERROR"
        exit 1
    }

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 2: Check and install Docker Desktop
    # ───────────────────────────────────────────────────────────────────────────

    if (-not (Test-DockerInstalled)) {
        Write-Host ""

        if (-not $Unattended) {
            Write-Host "  Docker Desktop is not installed." -ForegroundColor Yellow
            $response = Read-Host "  Download and install Docker Desktop now? (Y/n)"
            if ($response -eq "n" -or $response -eq "N") {
                Write-Log "Docker Desktop is required" "ERROR"
                exit 1
            }
        }

        $installed = Install-DockerDesktop -Backend $backend.Backend

        if (-not $installed) {
            Write-Log "Docker Desktop installation failed" "ERROR"
            exit 1
        }

        # Docker Desktop often needs a restart after first install
        Write-Host ""
        Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Yellow
        Write-Host "  Docker Desktop installed. A restart may be required." -ForegroundColor Yellow
        Write-Host "  If Docker fails to start, please restart and try again." -ForegroundColor Yellow
        Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Yellow
        Write-Host ""
    }

    Write-Log "Docker Desktop is installed" "OK"

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 3: Start Docker Desktop if not running
    # ───────────────────────────────────────────────────────────────────────────

    if (-not (Test-DockerRunning)) {
        $started = Start-DockerDesktop
        if (-not $started) {
            Write-Log "Please start Docker Desktop manually and run this script again" "ERROR"
            exit 1
        }
    } else {
        Write-Log "Docker Desktop is running" "OK"
    }

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 3.5: Check for port conflicts
    # ───────────────────────────────────────────────────────────────────────────

    # Port conflict check will use the selected coin (determined below)
    # Moved to after coin selection

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 4: Select coin (single-coin Docker mode)
    # ───────────────────────────────────────────────────────────────────────────

    $storageConfig = @{}
    $soloCoin = "DGB"

    if ($Unattended) {
        # Use command-line -Coin parameter
        $soloCoin = $Coin
    } else {
        # Interactive mode: show limitations panel, then coin selection
        Show-DockerLimitations
        $soloCoin = Show-SoloCoinMenu
    }

    # Block SYS - it is merge-mining only and cannot be mined standalone in Docker
    if ($soloCoin -eq "SYS") {
        Write-Host ""
        Write-Host "  Syscoin (SYS) is a merge-mining-only coin and cannot be mined standalone." -ForegroundColor Red
        Write-Host "  SYS must be merge-mined with BTC using the native Linux installer (install.sh)." -ForegroundColor Yellow
        Write-Host "  Docker single-coin mode does not support merge-mining." -ForegroundColor Yellow
        Write-Host ""
        Write-Log "SYS is not available in Docker single-coin mode (merge-mining only)" "ERROR"
        exit 1
    }

    # Validate coin exists in config
    if (-not $Script:CoinConfig.ContainsKey($soloCoin)) {
        Write-Log "Unknown coin: $soloCoin" "ERROR"
        exit 1
    }

    $coinInfo = $Script:CoinConfig[$soloCoin]
    Write-Log "Selected: $soloCoin ($($coinInfo.Algo)) - Profile: $($coinInfo.Profile)" "OK"

    # Check port conflicts for the selected coin
    if (-not (Test-PortAvailability -SelectedCoin $soloCoin)) {
        Write-Log "Port conflicts detected - installation aborted by user" "WARN"
        exit 1
    }

    # Get storage location (interactive only)
    if (-not $Unattended) {
        $storageConfig = Get-BlockchainStorageLocations -SelectedCoin $soloCoin
    }

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 5: Get pool configuration
    # ───────────────────────────────────────────────────────────────────────────

    $interactive = -not $Unattended
    $config = Get-PoolConfiguration -Address $PoolAddress -CoinType $soloCoin -Interactive $interactive

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 6: Installation Summary & Confirmation
    # ───────────────────────────────────────────────────────────────────────────

    if (-not $Unattended) {
        Write-Host ""
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
        Write-Host "    INSTALLATION SUMMARY" -ForegroundColor White
        Write-Host "  ═══════════════════════════════════════════════════════════════" -ForegroundColor Magenta
        Write-Host ""
        Write-Host "  Coin:             $soloCoin ($($coinInfo.Algo))" -ForegroundColor White
        Write-Host "  Profile:          $($coinInfo.Profile)" -ForegroundColor White
        Write-Host "  Install Location: $Script:InstallDir" -ForegroundColor White
        Write-Host "  Pool Address:     $($config.PoolAddress)" -ForegroundColor White
        Write-Host "  Stratum V1:       $($config.StratumPort)" -ForegroundColor White
        Write-Host "  Stratum TLS:      $($config.StratumTlsPort)" -ForegroundColor White
        Write-Host ""

        if ($storageConfig.Count -gt 0) {
            Write-Host "  Blockchain Storage:" -ForegroundColor Yellow
            foreach ($key in $storageConfig.Keys) {
                Write-Host "    - $key : $($storageConfig[$key])" -ForegroundColor Green
            }
            Write-Host ""
        }

        $confirm = Read-Host "  Proceed with installation? (Y/n)"
        if ($confirm -eq "n" -or $confirm -eq "N") {
            Write-Host "  Installation cancelled." -ForegroundColor Yellow
            exit 0
        }
    }

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 7: Configure networking and firewall
    # ───────────────────────────────────────────────────────────────────────────

    if ($backend.Backend -eq "WSL2") {
        Configure-WSL2Networking -SelectedCoin $soloCoin
    }

    # Configure firewall for selected coin
    Configure-Firewall -Config $config -EnabledCoins @($soloCoin)

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 8: Initialize Docker environment
    # ───────────────────────────────────────────────────────────────────────────

    Initialize-DockerEnvironment -Config $config

    # Generate .env file with coin-specific settings
    New-EnvironmentFile -Config $config -StorageConfig $storageConfig

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 9: Start Spiral Pool
    # ───────────────────────────────────────────────────────────────────────────

    $started = Start-SpiralPool -Profile $coinInfo.Profile
    if (-not $started) {
        Write-Log "Failed to start Spiral Pool containers" "ERROR"
        exit 1
    }

    # ───────────────────────────────────────────────────────────────────────────
    # STEP 10: Configure persistence
    # ───────────────────────────────────────────────────────────────────────────

    Configure-Persistence -Profile $coinInfo.Profile

    # ───────────────────────────────────────────────────────────────────────────
    # DONE
    # ───────────────────────────────────────────────────────────────────────────

    Show-Summary -Config $config
    Write-Log "Installation complete" "OK"
}

# Run main with error handling
try {
    Main
} catch {
    Write-Host ""
    Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Red
    Write-Host "  INSTALLATION FAILED" -ForegroundColor Red
    Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Red
    Write-Host ""
    Write-Host "  Error: $_" -ForegroundColor Red
    Write-Host "  Log:   $Script:LogFile" -ForegroundColor Yellow
    Write-Host ""
    exit 1
}
