# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Configure Windows Firewall rules for a new Spiral Pool coin.

.DESCRIPTION
    This script creates Windows Firewall inbound rules for a newly added
    cryptocurrency's stratum ports. It reads the coin's configuration from
    the manifest (mining section) and creates rules for Stratum V1, V2, and TLS ports.

    The script is called automatically by spiralpool-add-coin.bat after
    successfully generating coin support files.

.PARAMETER Symbol
    The coin ticker symbol (e.g., NEWCOIN, FOO, etc.)

.PARAMETER StratumV1Port
    Stratum V1 port (miners connect here). If not specified, reads from manifest.

.PARAMETER StratumV2Port
    Stratum V2 binary protocol port. Defaults to StratumV1Port + 1.

.PARAMETER StratumTlsPort
    Stratum TLS encrypted port. Defaults to StratumV1Port + 2.

.PARAMETER FirewallProfiles
    Network profiles to create rules for. Defaults to "Private,Domain".
    Options: Private, Domain, Public (or combinations like "Private,Domain,Public")

.PARAMETER Force
    Overwrite existing firewall rules without prompting.

.EXAMPLE
    .\configure-coin-firewall.ps1 -Symbol NEWCOIN -StratumV1Port 18335

.EXAMPLE
    .\configure-coin-firewall.ps1 -Symbol FOO -StratumV1Port 19335 -Force

.EXAMPLE
    .\configure-coin-firewall.ps1 -Symbol BAR -FirewallProfiles "Private,Domain,Public"

.NOTES
    - Requires Administrator privileges
    - By default, creates rules for Private and Domain network profiles only (not Public)
    - Profiles can be configured per-coin in the manifest's mining.firewall_profiles
    - Rules are named "Spiral Pool - <SYMBOL> Stratum V1/V2/TLS"
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)]
    [ValidatePattern('^[A-Z0-9]{2,10}$')]
    [string]$Symbol,

    [Parameter(Mandatory=$false)]
    [ValidateRange(1024, 65535)]
    [int]$StratumV1Port = 0,

    [Parameter(Mandatory=$false)]
    [ValidateRange(1024, 65535)]
    [int]$StratumV2Port = 0,

    [Parameter(Mandatory=$false)]
    [ValidateRange(1024, 65535)]
    [int]$StratumTlsPort = 0,

    [Parameter(Mandatory=$false)]
    [string]$FirewallProfiles = "",

    [switch]$Force
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
    }

    $color = $colors[$Level]
    if (-not $color) { $color = "White" }

    $prefix = switch ($Level) {
        "OK"    { "[+]" }
        "WARN"  { "[!]" }
        "ERROR" { "[-]" }
        "STEP"  { "[*]" }
        default { "[i]" }
    }

    Write-Host "  $prefix $Message" -ForegroundColor $color
}

function Get-CoinConfigFromManifest {
    param([string]$Symbol)

    $scriptDir = if ($PSScriptRoot) { $PSScriptRoot } else { Split-Path -Parent $MyInvocation.ScriptName }
    $manifestPath = Join-Path (Split-Path -Parent (Split-Path -Parent $scriptDir)) "config\coins.manifest.yaml"

    if (-not (Test-Path $manifestPath)) {
        return $null
    }

    # Simple YAML parsing for mining section
    $content = Get-Content $manifestPath -Raw

    # Split manifest into per-coin blocks and find the target coin
    $coinBlocks = $content -split "(?=\s+-\s+symbol:)"
    $coinBlock = $null
    foreach ($block in $coinBlocks) {
        if ($block -match "symbol:\s*$Symbol\b") {
            $coinBlock = $block
            break
        }
    }
    if (-not $coinBlock) {
        return $null
    }

    $result = @{
        StratumV1 = 0
        StratumV2 = 0
        StratumTls = 0
        FirewallProfiles = @("Private", "Domain")  # Default profiles
    }

    # Extract ports from this coin's block only
    if ($coinBlock -match "stratum_port:\s*(\d+)") {
        $result.StratumV1 = [int]$Matches[1]
    }

    if ($coinBlock -match "stratum_v2_port:\s*(\d+)") {
        $result.StratumV2 = [int]$Matches[1]
    } elseif ($result.StratumV1 -gt 0) {
        $result.StratumV2 = $result.StratumV1 + 1
    }

    if ($coinBlock -match "stratum_tls_port:\s*(\d+)") {
        $result.StratumTls = [int]$Matches[1]
    } elseif ($result.StratumV1 -gt 0) {
        $result.StratumTls = $result.StratumV1 + 2
    }

    # Try to extract firewall_profiles — check coin block first, then global default
    $profilesFound = $false
    if ($coinBlock -match "firewall_profiles:\s*\n((?:\s*-\s*\w+\s*\n?)+)") {
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
            $result.FirewallProfiles = $profilesList
            $profilesFound = $true
        }
    }
    if (-not $profilesFound -and $content -match "default_firewall_profiles:\s*\n((?:\s*-\s*\w+\s*\n)+)") {
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
            $result.FirewallProfiles = $profilesList
        }
    }

    if ($result.StratumV1 -eq 0) {
        return $null
    }

    return $result
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN SCRIPT
# ═══════════════════════════════════════════════════════════════════════════════

Write-Host ""
Write-Host "═══════════════════════════════════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host "  Spiral Pool - Firewall Configuration for $Symbol" -ForegroundColor White
Write-Host "═══════════════════════════════════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host ""

# Default firewall profiles
$networkProfiles = @("Private", "Domain")

# Determine ports and profiles from manifest or command line
if ($StratumV1Port -eq 0 -or [string]::IsNullOrEmpty($FirewallProfiles)) {
    Write-Log "Reading configuration from manifest..." "INFO"
    $manifestConfig = Get-CoinConfigFromManifest -Symbol $Symbol

    if ($manifestConfig) {
        if ($StratumV1Port -eq 0) {
            $StratumV1Port = $manifestConfig.StratumV1
            $StratumV2Port = $manifestConfig.StratumV2
            $StratumTlsPort = $manifestConfig.StratumTls
            Write-Log "Found ports in manifest: V1=$StratumV1Port, V2=$StratumV2Port, TLS=$StratumTlsPort" "OK"
        }
        if ([string]::IsNullOrEmpty($FirewallProfiles) -and $manifestConfig.FirewallProfiles) {
            $networkProfiles = $manifestConfig.FirewallProfiles
        }
    } elseif ($StratumV1Port -eq 0) {
        Write-Log "Could not find $Symbol in manifest. Please specify -StratumV1Port" "ERROR"
        exit 1
    }
}

# Override profiles from command line if specified
if (-not [string]::IsNullOrEmpty($FirewallProfiles)) {
    $networkProfiles = $FirewallProfiles -split "," | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" } | ForEach-Object {
        $_.Substring(0,1).ToUpper() + $_.Substring(1).ToLower()
    }
}

# Set default V2 and TLS ports if not specified
if ($StratumV2Port -eq 0) {
    $StratumV2Port = $StratumV1Port + 1
}
if ($StratumTlsPort -eq 0) {
    $StratumTlsPort = $StratumV1Port + 2
}

# Convert profile array to comma-separated string for firewall rule
$profileString = $networkProfiles -join ","

Write-Log "Configuring firewall for $Symbol" "STEP"
Write-Log "  Stratum V1:  $StratumV1Port/TCP" "INFO"
Write-Log "  Stratum V2:  $StratumV2Port/TCP" "INFO"
Write-Log "  Stratum TLS: $StratumTlsPort/TCP" "INFO"
Write-Log "  Profiles:    $profileString" "INFO"
Write-Host ""

# Check for existing rules
$existingRules = Get-NetFirewallRule -DisplayName "Spiral Pool - $Symbol*" -ErrorAction SilentlyContinue

if ($existingRules -and -not $Force) {
    Write-Log "Firewall rules for $Symbol already exist." "WARN"
    Write-Host ""
    $response = Read-Host "  Overwrite existing rules? [y/N]"
    if ($response -ne 'y' -and $response -ne 'Y') {
        Write-Log "Aborted. Use -Force to overwrite without prompting." "INFO"
        exit 0
    }
}

# Remove existing rules if present
if ($existingRules) {
    Write-Log "Removing existing firewall rules for $Symbol..." "INFO"
    $existingRules | Remove-NetFirewallRule -ErrorAction SilentlyContinue
}

# Create new rules
$rules = @(
    @{
        Name = "$Symbol Stratum V1"
        Port = $StratumV1Port
        Desc = "$Symbol mining connections (Stratum V1)"
    }
    @{
        Name = "$Symbol Stratum V2"
        Port = $StratumV2Port
        Desc = "$Symbol mining connections (Stratum V2 binary)"
    }
    @{
        Name = "$Symbol Stratum TLS"
        Port = $StratumTlsPort
        Desc = "$Symbol encrypted mining connections"
    }
)

try {
    foreach ($rule in $rules) {
        # Create rule for configured network profiles
        New-NetFirewallRule `
            -DisplayName "Spiral Pool - $($rule.Name)" `
            -Direction Inbound `
            -Protocol TCP `
            -LocalPort $rule.Port `
            -Action Allow `
            -Profile $profileString `
            -Description $rule.Desc | Out-Null

        Write-Log "Created: $($rule.Name) on port $($rule.Port)" "OK"
    }

    Write-Host ""
    Write-Log "Firewall rules created successfully!" "OK"
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
    Write-Log "Failed to create firewall rules: $_" "ERROR"
    Write-Host ""
    Write-Host "  You may need to manually create firewall rules:" -ForegroundColor Yellow
    Write-Host "    - Spiral Pool - $Symbol Stratum V1: $StratumV1Port/TCP ($profileString)" -ForegroundColor DarkGray
    Write-Host "    - Spiral Pool - $Symbol Stratum V2: $StratumV2Port/TCP ($profileString)" -ForegroundColor DarkGray
    Write-Host "    - Spiral Pool - $Symbol Stratum TLS: $StratumTlsPort/TCP ($profileString)" -ForegroundColor DarkGray
    Write-Host ""
    exit 1
}

Write-Host "═══════════════════════════════════════════════════════════════════════════════" -ForegroundColor Cyan
