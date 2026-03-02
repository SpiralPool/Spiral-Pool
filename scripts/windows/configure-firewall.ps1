# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Configure Windows Firewall rules for Spiral Pool based on enabled coins.

.DESCRIPTION
    This script reads the coin manifest (coins.manifest.yaml) and creates
    Windows Firewall inbound rules for all necessary ports:

    - Stratum ports (V1, V2, TLS) for each enabled coin
    - RPC ports for blockchain nodes
    - ZMQ ports for block notifications
    - Common ports (API, Dashboard, Metrics)

    It automatically detects which coins are enabled and configures only
    the necessary ports, avoiding unnecessary firewall exposure.

.PARAMETER EnabledCoins
    Array of coin symbols to enable (e.g., "DGB", "BTC", "LTC").
    Use "ALL" to enable all coins in the manifest.
    If not specified, prompts interactively or uses defaults.

.PARAMETER FirewallProfiles
    Network profiles to create rules for. Defaults to "Private,Domain".
    Options: Private, Domain, Public (or combinations)

.PARAMETER IncludeRPC
    Include RPC ports in firewall rules (for external node access).
    Default: $false (RPC typically accessed only locally or via Docker network)

.PARAMETER IncludeZMQ
    Include ZMQ ports in firewall rules (for external block notifications).
    Default: $false (ZMQ typically accessed only via Docker network)

.PARAMETER Force
    Overwrite existing firewall rules without prompting.

.PARAMETER ListCoins
    List all available coins from the manifest and exit.

.PARAMETER DryRun
    Show what rules would be created without actually creating them.

.EXAMPLE
    .\configure-firewall.ps1 -EnabledCoins DGB -Force

.EXAMPLE
    .\configure-firewall.ps1 -EnabledCoins DGB,BTC,LTC -FirewallProfiles "Private,Domain"

.EXAMPLE
    .\configure-firewall.ps1 -EnabledCoins ALL -IncludeRPC -IncludeZMQ

.EXAMPLE
    .\configure-firewall.ps1 -ListCoins

.EXAMPLE
    .\configure-firewall.ps1 -EnabledCoins DGB -DryRun

.NOTES
    - Requires Administrator privileges
    - By default, creates rules for Private and Domain network profiles only
    - Rules are named "Spiral Pool - <Description>"
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory=$false)]
    [string[]]$EnabledCoins = @(),

    [Parameter(Mandatory=$false)]
    [string]$FirewallProfiles = "Private,Domain",

    [switch]$IncludeRPC,
    [switch]$IncludeZMQ,
    [switch]$Force,
    [switch]$ListCoins,
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

# ═══════════════════════════════════════════════════════════════════════════════
# HELPER FUNCTIONS
# ═══════════════════════════════════════════════════════════════════════════════

function Write-Log {
    param([string]$Message, [string]$Level = "INFO")

    $colors = @{
        "INFO"  = "Cyan"
        "OK"    = "Green"
        "WARN"  = "Yellow"
        "ERROR" = "Red"
        "STEP"  = "Magenta"
        "DRY"   = "DarkYellow"
    }

    $color = $colors[$Level]
    if (-not $color) { $color = "White" }

    $prefix = switch ($Level) {
        "OK"    { "[+]" }
        "WARN"  { "[!]" }
        "ERROR" { "[-]" }
        "STEP"  { "[*]" }
        "DRY"   { "[DRY]" }
        default { "[i]" }
    }

    Write-Host "  $prefix $Message" -ForegroundColor $color
}

function Get-ManifestPath {
    $scriptDir = Split-Path -Parent $MyInvocation.ScriptName
    $manifestPath = Join-Path (Split-Path -Parent (Split-Path -Parent $scriptDir)) "config\coins.manifest.yaml"

    if (-not (Test-Path $manifestPath)) {
        # Try alternative paths
        $altPaths = @(
            (Join-Path $PSScriptRoot "..\..\config\coins.manifest.yaml"),
            (Join-Path $PSScriptRoot "..\..\..\config\coins.manifest.yaml"),
            ".\config\coins.manifest.yaml",
            "..\config\coins.manifest.yaml"
        )

        foreach ($path in $altPaths) {
            if (Test-Path $path) {
                $manifestPath = (Resolve-Path $path).Path
                break
            }
        }
    }

    return $manifestPath
}

function Parse-CoinManifest {
    param([string]$ManifestPath)

    if (-not (Test-Path $ManifestPath)) {
        Write-Log "Manifest not found at: $ManifestPath" "ERROR"
        return $null
    }

    $content = Get-Content $ManifestPath -Raw
    $coins = @()

    # Parse each coin section
    # Split by "- symbol:" to get individual coin blocks
    $coinBlocks = $content -split "(?=\s+-\s+symbol:)"

    foreach ($block in $coinBlocks) {
        if ($block -notmatch "symbol:\s*(\w+)") { continue }

        $symbol = $Matches[1]

        $coin = @{
            Symbol = $symbol
            Name = ""
            Algorithm = ""
            StratumV1 = 0
            StratumV2 = 0
            StratumTls = 0
            RpcPort = 0
            P2pPort = 0
            ZmqPort = 0
            FirewallProfiles = @("Private", "Domain")
        }

        # Parse name
        if ($block -match "name:\s*([^\r\n]+)") {
            $coin.Name = $Matches[1].Trim()
        }

        # Parse algorithm
        if ($block -match "algorithm:\s*(\w+)") {
            $coin.Algorithm = $Matches[1]
        }

        # Parse network ports
        if ($block -match "rpc_port:\s*(\d+)") {
            $coin.RpcPort = [int]$Matches[1]
        }
        if ($block -match "p2p_port:\s*(\d+)") {
            $coin.P2pPort = [int]$Matches[1]
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
        }

        # Parse coin-specific firewall profiles if present
        if ($block -match "firewall_profiles:\s*\n((?:\s*-\s*\w+\s*\n?)+)") {
            $profilesText = $Matches[1]
            $profilesList = @()
            foreach ($line in $profilesText -split "`n") {
                if ($line -match "^\s*-\s*(\w+)") {
                    $profileName = $Matches[1]
                    $profileName = $profileName.Substring(0,1).ToUpper() + $profileName.Substring(1).ToLower()
                    $profilesList += $profileName
                }
            }
            if ($profilesList.Count -gt 0) {
                $coin.FirewallProfiles = $profilesList
            }
        }

        if ($coin.StratumV1 -gt 0) {
            $coins += $coin
        }
    }

    return $coins
}

function Get-GlobalFirewallProfiles {
    param([string]$ManifestPath)

    $content = Get-Content $ManifestPath -Raw
    $profiles = @("Private", "Domain")

    if ($content -match "default_firewall_profiles:\s*\n((?:\s*-\s*\w+\s*\n?)+)") {
        $profilesText = $Matches[1]
        $profilesList = @()
        foreach ($line in $profilesText -split "`n") {
            if ($line -match "^\s*-\s*(\w+)") {
                $profileName = $Matches[1]
                $profileName = $profileName.Substring(0,1).ToUpper() + $profileName.Substring(1).ToLower()
                $profilesList += $profileName
            }
        }
        if ($profilesList.Count -gt 0) {
            $profiles = $profilesList
        }
    }

    return $profiles
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN SCRIPT
# ═══════════════════════════════════════════════════════════════════════════════

Write-Host ""
Write-Host "═══════════════════════════════════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host "  Spiral Pool - Windows Firewall Configuration" -ForegroundColor White
Write-Host "═══════════════════════════════════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host ""

# Find and parse manifest
$manifestPath = Get-ManifestPath
if (-not (Test-Path $manifestPath)) {
    Write-Log "Cannot find coins.manifest.yaml" "ERROR"
    Write-Host "  Searched paths:" -ForegroundColor DarkGray
    Write-Host "    - $manifestPath" -ForegroundColor DarkGray
    exit 1
}

Write-Log "Reading manifest: $manifestPath" "INFO"
$allCoins = Parse-CoinManifest -ManifestPath $manifestPath
$globalProfiles = Get-GlobalFirewallProfiles -ManifestPath $manifestPath

if (-not $allCoins -or $allCoins.Count -eq 0) {
    Write-Log "No coins found in manifest" "ERROR"
    exit 1
}

Write-Log "Found $($allCoins.Count) coins in manifest" "OK"

# Handle -ListCoins
if ($ListCoins) {
    Write-Host ""
    Write-Host "  Available coins:" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  Symbol    Algorithm   Stratum     RPC     ZMQ     Name" -ForegroundColor DarkGray
    Write-Host "  ──────────────────────────────────────────────────────────────────" -ForegroundColor DarkGray

    foreach ($coin in $allCoins | Sort-Object Symbol) {
        $sym = $coin.Symbol.PadRight(10)
        $alg = $coin.Algorithm.PadRight(12)
        $str = "$($coin.StratumV1)".PadRight(8)
        $rpc = "$($coin.RpcPort)".PadRight(8)
        $zmq = "$($coin.ZmqPort)".PadRight(8)
        Write-Host "  $sym$alg$str$rpc$zmq$($coin.Name)"
    }
    Write-Host ""
    exit 0
}

# Determine which coins to enable
$selectedCoins = @()

if ($EnabledCoins.Count -eq 0) {
    # No coins specified - prompt or use DGB as default
    Write-Log "No coins specified. Use -EnabledCoins parameter or -ListCoins to see available coins." "WARN"
    Write-Host ""
    Write-Host "  Example: .\configure-firewall.ps1 -EnabledCoins DGB,BTC" -ForegroundColor DarkGray
    Write-Host "  Example: .\configure-firewall.ps1 -EnabledCoins ALL" -ForegroundColor DarkGray
    Write-Host ""
    exit 1
}

if ($EnabledCoins -contains "ALL") {
    $selectedCoins = $allCoins
    Write-Log "Enabling ALL coins ($($selectedCoins.Count) coins)" "INFO"
} else {
    foreach ($symbol in $EnabledCoins) {
        $coin = $allCoins | Where-Object { $_.Symbol -eq $symbol.ToUpper() }
        if ($coin) {
            $selectedCoins += $coin
        } else {
            Write-Log "Unknown coin: $symbol (skipping)" "WARN"
        }
    }
}

if ($selectedCoins.Count -eq 0) {
    Write-Log "No valid coins selected" "ERROR"
    exit 1
}

Write-Log "Selected coins: $($selectedCoins.Symbol -join ', ')" "OK"
Write-Host ""

# Parse firewall profiles
$networkProfiles = $FirewallProfiles -split "," | ForEach-Object {
    $_.Trim().Substring(0,1).ToUpper() + $_.Trim().Substring(1).ToLower()
}
$profileString = $networkProfiles -join ","

Write-Log "Firewall profiles: $profileString" "INFO"

# Build list of all rules to create
$rules = @()

# Common ports (always included)
$rules += @{ Name = "REST API"; Port = 4000; Desc = "Pool statistics API"; Category = "Common" }
$rules += @{ Name = "Dashboard"; Port = 1618; Desc = "Web dashboard"; Category = "Common" }
$rules += @{ Name = "Metrics"; Port = 9100; Desc = "Prometheus metrics"; Category = "Common" }

# Coin-specific ports
foreach ($coin in $selectedCoins) {
    $sym = $coin.Symbol

    # Stratum ports (always included for mining)
    if ($coin.StratumV1 -gt 0) {
        $rules += @{ Name = "$sym Stratum V1"; Port = $coin.StratumV1; Desc = "$($coin.Name) mining connections (Stratum V1)"; Category = "Stratum" }
    }
    if ($coin.StratumV2 -gt 0) {
        $rules += @{ Name = "$sym Stratum V2"; Port = $coin.StratumV2; Desc = "$($coin.Name) mining connections (Stratum V2)"; Category = "Stratum" }
    }
    if ($coin.StratumTls -gt 0) {
        $rules += @{ Name = "$sym Stratum TLS"; Port = $coin.StratumTls; Desc = "$($coin.Name) encrypted mining connections"; Category = "Stratum" }
    }

    # RPC ports (optional - typically internal only)
    if ($IncludeRPC -and $coin.RpcPort -gt 0) {
        $rules += @{ Name = "$sym RPC"; Port = $coin.RpcPort; Desc = "$($coin.Name) blockchain RPC"; Category = "RPC" }
    }

    # ZMQ ports (optional - typically internal only)
    if ($IncludeZMQ -and $coin.ZmqPort -gt 0) {
        $rules += @{ Name = "$sym ZMQ"; Port = $coin.ZmqPort; Desc = "$($coin.Name) block notifications (ZMQ)"; Category = "ZMQ" }
    }
}

# Remove duplicate ports (e.g., DGB and DGB-SCRYPT share the same node)
$uniqueRules = @()
$seenPorts = @{}
foreach ($rule in $rules) {
    $key = "$($rule.Port)"
    if (-not $seenPorts.ContainsKey($key)) {
        $seenPorts[$key] = $true
        $uniqueRules += $rule
    }
}
$rules = $uniqueRules

Write-Log "Total firewall rules to create: $($rules.Count)" "INFO"
Write-Host ""

# Group rules by category for display
$categories = $rules | Group-Object Category

foreach ($cat in $categories) {
    Write-Host "  $($cat.Name) Ports:" -ForegroundColor Yellow
    foreach ($rule in $cat.Group) {
        Write-Host "    - $($rule.Port)/TCP  $($rule.Name)" -ForegroundColor DarkGray
    }
    Write-Host ""
}

# Dry run - just show what would be done
if ($DryRun) {
    Write-Log "[DRY RUN] Would create the following firewall rules:" "DRY"
    Write-Host ""
    foreach ($rule in $rules) {
        Write-Host "    New-NetFirewallRule -DisplayName 'Spiral Pool - $($rule.Name)' \" -ForegroundColor DarkYellow
        Write-Host "        -Direction Inbound -Protocol TCP -LocalPort $($rule.Port) \" -ForegroundColor DarkYellow
        Write-Host "        -Action Allow -Profile $profileString" -ForegroundColor DarkYellow
        Write-Host ""
    }
    Write-Log "[DRY RUN] No changes made" "DRY"
    exit 0
}

# Check for existing rules
$existingRules = Get-NetFirewallRule -DisplayName "Spiral Pool*" -ErrorAction SilentlyContinue

if ($existingRules -and -not $Force) {
    Write-Log "Existing Spiral Pool firewall rules found: $($existingRules.Count)" "WARN"
    Write-Host ""
    $response = Read-Host "  Overwrite existing rules? [y/N]"
    if ($response -ne 'y' -and $response -ne 'Y') {
        Write-Log "Aborted. Use -Force to overwrite without prompting." "INFO"
        exit 0
    }
}

# Remove existing rules
if ($existingRules) {
    Write-Log "Removing existing Spiral Pool firewall rules..." "INFO"
    $existingRules | Remove-NetFirewallRule -ErrorAction SilentlyContinue
}

# Create new rules
Write-Log "Creating firewall rules..." "STEP"
Write-Host ""

$created = 0
$failed = 0

try {
    foreach ($rule in $rules) {
        try {
            New-NetFirewallRule `
                -DisplayName "Spiral Pool - $($rule.Name)" `
                -Direction Inbound `
                -Protocol TCP `
                -LocalPort $rule.Port `
                -Action Allow `
                -Profile $profileString `
                -Description $rule.Desc | Out-Null

            Write-Log "Created: $($rule.Name) on port $($rule.Port)" "OK"
            $created++
        } catch {
            Write-Log "Failed to create rule for $($rule.Name): $_" "ERROR"
            $failed++
        }
    }

    Write-Host ""
    Write-Log "Firewall configuration complete!" "OK"
    Write-Log "Rules created: $created, Failed: $failed" "INFO"
    Write-Host ""

    # Show appropriate message based on profiles
    if ($networkProfiles -contains "Public") {
        Write-Host "  WARNING: Rules include Public networks - use with caution!" -ForegroundColor Red
        Write-Host "           Public networks (coffee shops, airports) pose security risks." -ForegroundColor DarkGray
    } else {
        Write-Host "  Note: Rules apply to $profileString networks only." -ForegroundColor Yellow
        Write-Host "        Ports are NOT open on Public networks for security." -ForegroundColor DarkGray
    }
    Write-Host ""

} catch {
    Write-Log "Firewall configuration failed: $_" "ERROR"
    Write-Host ""
    Write-Host "  You may need to manually create firewall rules." -ForegroundColor Yellow
    Write-Host "  Run with -DryRun to see the commands needed." -ForegroundColor DarkGray
    Write-Host ""
    exit 1
}

Write-Host "═══════════════════════════════════════════════════════════════════════════════" -ForegroundColor Cyan
