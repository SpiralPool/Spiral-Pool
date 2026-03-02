# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Spiral Pool - Windows Deployment Test Matrix

.DESCRIPTION
    Comprehensive test suite for validating Windows Docker deployment.
    Tests installation prerequisites, Docker configuration, container health,
    network connectivity, and basic pool functionality.

    Supports all 13 coins via the -Coin parameter.

.NOTES
    Version: 1.1.0
    Author: Spiral Pool Contributors
    Status: EXPERIMENTAL TEST INFRASTRUCTURE

    Run after install-windows.ps1 to validate deployment.

.EXAMPLE
    # Run full test suite (default: DGB)
    .\test-windows-deployment.ps1

    # Run tests for Bitcoin
    .\test-windows-deployment.ps1 -Coin BTC

    # Run specific test category
    .\test-windows-deployment.ps1 -Category Docker -Coin LTC

    # Generate report only (no interactive prompts)
    .\test-windows-deployment.ps1 -NonInteractive -OutputReport "C:\test-results.json"
#>

param(
    [ValidateSet("All", "Prerequisites", "Docker", "Containers", "Network", "Health", "Performance")]
    [string]$Category = "All",

    [ValidateSet("DGB", "BTC", "BCH", "BC2", "LTC", "DOGE", "DGB-SCRYPT", "PEP", "CAT", "NMC", "SYS", "XMY", "FBTC")]
    [string]$Coin = "DGB",

    [switch]$NonInteractive,

    [string]$OutputReport = "",

    [string]$InstallDir = "C:\SpiralPool"
)

# ═══════════════════════════════════════════════════════════════════════════════
# CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════

$ErrorActionPreference = "Continue"
$Script:TestResults = @()
$Script:PassCount = 0
$Script:FailCount = 0
$Script:WarnCount = 0
$Script:SkipCount = 0

# Coin configuration table (mirrors install-windows.ps1)
$Script:CoinConfig = @{
    DGB          = @{ Container="digibyte";       RpcPort=14022; P2pPort=12024; StratumPort=3333;  TlsPort=3335;  Profile="dgb";        CliName="digibyte-cli";    ContainerName="spiralpool-digibyte" }
    BTC          = @{ Container="bitcoin";        RpcPort=8332;  P2pPort=8333;  StratumPort=4333;  TlsPort=4335;  Profile="btc";        CliName="bitcoin-cli";     ContainerName="spiralpool-bitcoin" }
    BCH          = @{ Container="bitcoincash";    RpcPort=8432;  P2pPort=8433;  StratumPort=5333;  TlsPort=5335;  Profile="bch";        CliName="bitcoin-cli";     ContainerName="spiralpool-bitcoincash" }
    BC2          = @{ Container="bitcoinii";      RpcPort=8339;  P2pPort=8338;  StratumPort=6333;  TlsPort=6335;  Profile="bc2";        CliName="bitcoinii-cli";   ContainerName="spiralpool-bitcoinii" }
    NMC          = @{ Container="namecoin";       RpcPort=8336;  P2pPort=8334;  StratumPort=14335; TlsPort=14337; Profile="nmc";        CliName="namecoin-cli";    ContainerName="spiralpool-namecoin" }
    SYS          = @{ Container="syscoin";        RpcPort=8370;  P2pPort=8369;  StratumPort=15335; TlsPort=15337; Profile="sys";        CliName="syscoin-cli";     ContainerName="spiralpool-syscoin" }
    XMY          = @{ Container="myriadcoin";     RpcPort=10889; P2pPort=10888; StratumPort=17335; TlsPort=17337; Profile="xmy";        CliName="myriadcoin-cli";  ContainerName="spiralpool-myriadcoin" }
    FBTC         = @{ Container="fractalbitcoin"; RpcPort=8340;  P2pPort=8341;  StratumPort=18335; TlsPort=18337; Profile="fbtc";       CliName="bitcoin-cli";     ContainerName="spiralpool-fractalbitcoin" }
    LTC          = @{ Container="litecoin";       RpcPort=9332;  P2pPort=9333;  StratumPort=7333;  TlsPort=7335;  Profile="ltc";        CliName="litecoin-cli";    ContainerName="spiralpool-litecoin" }
    DOGE         = @{ Container="dogecoin";       RpcPort=22555; P2pPort=22556; StratumPort=8335;  TlsPort=8342;  Profile="doge";       CliName="dogecoin-cli";    ContainerName="spiralpool-dogecoin" }
    "DGB-SCRYPT" = @{ Container="digibyte";       RpcPort=14022; P2pPort=12024; StratumPort=3336;  TlsPort=3338;  Profile="dgb-scrypt"; CliName="digibyte-cli";    ContainerName="spiralpool-digibyte" }
    PEP          = @{ Container="pepecoin";       RpcPort=33873; P2pPort=33874; StratumPort=10335; TlsPort=10337; Profile="pep";        CliName="pepecoin-cli";    ContainerName="spiralpool-pepecoin" }
    CAT          = @{ Container="catcoin";        RpcPort=9932;  P2pPort=9933;  StratumPort=12335; TlsPort=12337; Profile="cat";        CliName="catcoin-cli";     ContainerName="spiralpool-catcoin" }
}

$coinInfo = $Script:CoinConfig[$Coin]

# ═══════════════════════════════════════════════════════════════════════════════
# TEST INFRASTRUCTURE
# ═══════════════════════════════════════════════════════════════════════════════

function Write-TestHeader {
    param([string]$Title)
    Write-Host ""
    Write-Host "╔════════════════════════════════════════════════════════════════════════════╗" -ForegroundColor Cyan
    Write-Host "║  $($Title.PadRight(74))║" -ForegroundColor Cyan
    Write-Host "╚════════════════════════════════════════════════════════════════════════════╝" -ForegroundColor Cyan
    Write-Host ""
}

function Write-TestSection {
    param([string]$Title)
    Write-Host ""
    Write-Host "  ══════════════════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host "    $Title" -ForegroundColor White
    Write-Host "  ══════════════════════════════════════════════════════════════════════════" -ForegroundColor Magenta
    Write-Host ""
}

function Add-TestResult {
    param(
        [string]$Category,
        [string]$Test,
        [ValidateSet("PASS", "FAIL", "WARN", "SKIP")]
        [string]$Status,
        [string]$Message = "",
        [string]$Details = ""
    )

    $result = @{
        Category = $Category
        Test = $Test
        Status = $Status
        Message = $Message
        Details = $Details
        Timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    }

    $Script:TestResults += $result

    $statusColor = switch ($Status) {
        "PASS" { "Green"; $Script:PassCount++ }
        "FAIL" { "Red"; $Script:FailCount++ }
        "WARN" { "Yellow"; $Script:WarnCount++ }
        "SKIP" { "DarkGray"; $Script:SkipCount++ }
    }

    $statusSymbol = switch ($Status) {
        "PASS" { "[OK]" }
        "FAIL" { "[X]" }
        "WARN" { "[!]" }
        "SKIP" { "[-]" }
    }

    Write-Host "    $statusSymbol " -ForegroundColor $statusColor -NoNewline
    Write-Host "$Test" -ForegroundColor White -NoNewline
    if ($Message) {
        Write-Host " - $Message" -ForegroundColor DarkGray
    } else {
        Write-Host ""
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# TEST CATEGORY: PREREQUISITES
# ═══════════════════════════════════════════════════════════════════════════════

function Test-Prerequisites {
    Write-TestSection "PREREQUISITES"

    # Test 1: Windows Version
    $osInfo = Get-CimInstance -ClassName Win32_OperatingSystem
    $build = [System.Environment]::OSVersion.Version.Build
    if ($build -ge 22000) {
        Add-TestResult -Category "Prerequisites" -Test "Windows 11 (Build 22000+)" `
            -Status "PASS" -Message "Build $build"
    } else {
        Add-TestResult -Category "Prerequisites" -Test "Windows 11 (Build 22000+)" `
            -Status "FAIL" -Message "Build $build - Windows 11 required"
    }

    # Test 2: Administrator privileges
    $isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    if ($isAdmin) {
        Add-TestResult -Category "Prerequisites" -Test "Administrator privileges" -Status "PASS"
    } else {
        Add-TestResult -Category "Prerequisites" -Test "Administrator privileges" -Status "FAIL"
    }

    # Test 3: RAM
    $totalRAM = (Get-CimInstance -ClassName Win32_ComputerSystem).TotalPhysicalMemory
    $totalRAMGB = [math]::Round($totalRAM / 1GB, 1)
    if ($totalRAMGB -ge 16) {
        Add-TestResult -Category "Prerequisites" -Test "RAM >= 16GB (recommended)" `
            -Status "PASS" -Message "$totalRAMGB GB"
    } elseif ($totalRAMGB -ge 4) {
        Add-TestResult -Category "Prerequisites" -Test "RAM >= 16GB (recommended)" `
            -Status "WARN" -Message "$totalRAMGB GB - minimum met, recommended 16GB+"
    } else {
        Add-TestResult -Category "Prerequisites" -Test "RAM >= 16GB (recommended)" `
            -Status "FAIL" -Message "$totalRAMGB GB - minimum 4GB required"
    }

    # Test 4: WSL2 or Hyper-V
    $wsl2Installed = $false
    try {
        $wslOutput = wsl --status 2>&1
        $wsl2Installed = ($LASTEXITCODE -eq 0)
    } catch { }

    $hyperVEnabled = $false
    try {
        $hyperv = Get-WindowsOptionalFeature -FeatureName Microsoft-Hyper-V -Online -ErrorAction SilentlyContinue
        $hyperVEnabled = ($hyperv.State -eq "Enabled")
    } catch { }

    if ($wsl2Installed) {
        Add-TestResult -Category "Prerequisites" -Test "Virtualization backend" `
            -Status "PASS" -Message "WSL2 installed"
    } elseif ($hyperVEnabled) {
        Add-TestResult -Category "Prerequisites" -Test "Virtualization backend" `
            -Status "PASS" -Message "Hyper-V enabled"
    } else {
        Add-TestResult -Category "Prerequisites" -Test "Virtualization backend" `
            -Status "FAIL" -Message "Neither WSL2 nor Hyper-V detected"
    }

    # Test 5: Docker Desktop installed
    $dockerPaths = @(
        "$env:ProgramFiles\Docker\Docker\Docker Desktop.exe",
        "${env:ProgramFiles(x86)}\Docker\Docker\Docker Desktop.exe",
        "$env:LocalAppData\Docker\Docker Desktop.exe"
    )
    $dockerInstalled = $dockerPaths | Where-Object { Test-Path $_ } | Select-Object -First 1
    if ($dockerInstalled) {
        Add-TestResult -Category "Prerequisites" -Test "Docker Desktop installed" `
            -Status "PASS" -Message (Split-Path $dockerInstalled -Parent)
    } else {
        Add-TestResult -Category "Prerequisites" -Test "Docker Desktop installed" `
            -Status "FAIL" -Message "Docker Desktop not found"
    }

    # Test 6: Installation directory exists
    if (Test-Path $InstallDir) {
        Add-TestResult -Category "Prerequisites" -Test "Installation directory exists" `
            -Status "PASS" -Message $InstallDir
    } else {
        Add-TestResult -Category "Prerequisites" -Test "Installation directory exists" `
            -Status "FAIL" -Message "$InstallDir not found"
    }

    # Test 7: .env file exists (replaced Docker Secrets check)
    $envFile = Join-Path $InstallDir "docker\.env"
    if (Test-Path $envFile) {
        Add-TestResult -Category "Prerequisites" -Test "Environment file (.env)" `
            -Status "PASS" -Message ".env file present"
    } else {
        Add-TestResult -Category "Prerequisites" -Test "Environment file (.env)" `
            -Status "FAIL" -Message ".env file not found"
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# TEST CATEGORY: DOCKER
# ═══════════════════════════════════════════════════════════════════════════════

function Test-Docker {
    Write-TestSection "DOCKER CONFIGURATION"

    # Test 1: Docker daemon running
    try {
        $dockerInfo = docker info 2>&1
        if ($LASTEXITCODE -eq 0) {
            Add-TestResult -Category "Docker" -Test "Docker daemon running" -Status "PASS"
        } else {
            Add-TestResult -Category "Docker" -Test "Docker daemon running" `
                -Status "FAIL" -Message "Docker not responding"
            return  # Skip remaining Docker tests
        }
    } catch {
        Add-TestResult -Category "Docker" -Test "Docker daemon running" `
            -Status "FAIL" -Message $_.Exception.Message
        return
    }

    # Test 2: Docker version
    try {
        $dockerVersion = docker version --format '{{.Server.Version}}' 2>&1
        Add-TestResult -Category "Docker" -Test "Docker version" `
            -Status "PASS" -Message "v$dockerVersion"
    } catch {
        Add-TestResult -Category "Docker" -Test "Docker version" -Status "WARN"
    }

    # Test 3: Docker Compose available
    try {
        $composeVersion = docker compose version --short 2>&1
        if ($LASTEXITCODE -eq 0) {
            Add-TestResult -Category "Docker" -Test "Docker Compose available" `
                -Status "PASS" -Message "v$composeVersion"
        } else {
            Add-TestResult -Category "Docker" -Test "Docker Compose available" `
                -Status "FAIL" -Message "docker compose not found"
        }
    } catch {
        Add-TestResult -Category "Docker" -Test "Docker Compose available" `
            -Status "FAIL" -Message $_.Exception.Message
    }

    # Test 4: Windows compose file exists
    $composeFile = Join-Path $InstallDir "docker\docker-compose.windows.yml"
    if (Test-Path $composeFile) {
        Add-TestResult -Category "Docker" -Test "Windows compose file exists" `
            -Status "PASS" -Message $composeFile
    } else {
        Add-TestResult -Category "Docker" -Test "Windows compose file exists" `
            -Status "FAIL" -Message "docker-compose.windows.yml not found"
    }

    # Test 5: Bridge network created
    try {
        $networks = docker network ls --filter "name=spiralpool" --format "{{.Name}}" 2>&1
        if ($networks -match "spiralpool") {
            Add-TestResult -Category "Docker" -Test "Bridge network 'spiralpool-network'" `
                -Status "PASS"
        } else {
            Add-TestResult -Category "Docker" -Test "Bridge network 'spiralpool-network'" `
                -Status "WARN" -Message "Not created yet (created on first start)"
        }
    } catch {
        Add-TestResult -Category "Docker" -Test "Bridge network 'spiralpool-network'" `
            -Status "SKIP"
    }

    # Test 6: .env configuration (replaced secrets check)
    try {
        $envFile = Join-Path $InstallDir "docker\.env"
        if (Test-Path $envFile) {
            $envContent = Get-Content $envFile -Raw -ErrorAction SilentlyContinue
            if ($envContent -match "POOL_ADDRESS=" -and $envContent -match "RPC_PASSWORD=") {
                Add-TestResult -Category "Docker" -Test "Environment configuration" `
                    -Status "PASS" -Message "Using .env file"
            } else {
                Add-TestResult -Category "Docker" -Test "Environment configuration" `
                    -Status "WARN" -Message ".env file incomplete"
            }
        } else {
            Add-TestResult -Category "Docker" -Test "Environment configuration" `
                -Status "FAIL" -Message ".env file not found"
        }
    } catch {
        Add-TestResult -Category "Docker" -Test "Environment configuration" `
            -Status "SKIP"
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# TEST CATEGORY: CONTAINERS (dynamic per coin)
# ═══════════════════════════════════════════════════════════════════════════════

function Test-Containers {
    Write-TestSection "CONTAINER STATUS ($Coin)"

    $expectedContainers = @(
        @{ Name = $coinInfo.ContainerName; Service = "$Coin Blockchain Node" }
        @{ Name = "spiralpool-postgres"; Service = "PostgreSQL Database" }
        @{ Name = "spiralpool-stratum"; Service = "Stratum Server" }
        @{ Name = "spiralpool-dashboard"; Service = "Dashboard" }
        @{ Name = "spiralpool-sentinel"; Service = "Sentinel Monitor" }
    )

    foreach ($container in $expectedContainers) {
        try {
            $status = docker inspect --format '{{.State.Status}}' $container.Name 2>&1
            $health = docker inspect --format '{{.State.Health.Status}}' $container.Name 2>&1

            if ($status -eq "running") {
                if ($health -eq "healthy") {
                    Add-TestResult -Category "Containers" -Test $container.Service `
                        -Status "PASS" -Message "Running, Healthy"
                } elseif ($health -eq "unhealthy") {
                    Add-TestResult -Category "Containers" -Test $container.Service `
                        -Status "WARN" -Message "Running, Unhealthy"
                } else {
                    Add-TestResult -Category "Containers" -Test $container.Service `
                        -Status "PASS" -Message "Running"
                }
            } elseif ($status -match "exited|stopped") {
                Add-TestResult -Category "Containers" -Test $container.Service `
                    -Status "FAIL" -Message "Stopped"
            } else {
                Add-TestResult -Category "Containers" -Test $container.Service `
                    -Status "WARN" -Message "Status: $status"
            }
        } catch {
            Add-TestResult -Category "Containers" -Test $container.Service `
                -Status "SKIP" -Message "Container not found"
        }
    }

    # Volume check
    Write-Host ""
    Write-Host "    Volume Status:" -ForegroundColor DarkGray

    $volumeName = "spiralpool-$($coinInfo.Container)-data"
    $volumes = @($volumeName, "spiralpool-postgres-data", "spiralpool-stratum-config")
    foreach ($vol in $volumes) {
        try {
            $volInfo = docker volume inspect $vol 2>&1
            if ($LASTEXITCODE -eq 0) {
                Add-TestResult -Category "Containers" -Test "Volume: $vol" -Status "PASS"
            } else {
                Add-TestResult -Category "Containers" -Test "Volume: $vol" -Status "SKIP"
            }
        } catch {
            Add-TestResult -Category "Containers" -Test "Volume: $vol" -Status "SKIP"
        }
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# TEST CATEGORY: NETWORK (dynamic per coin)
# ═══════════════════════════════════════════════════════════════════════════════

function Test-Network {
    Write-TestSection "NETWORK CONNECTIVITY ($Coin)"

    $ports = @(
        @{ Port = $coinInfo.StratumPort; Service = "$Coin Stratum V1" }
        @{ Port = $coinInfo.TlsPort; Service = "$Coin Stratum TLS" }
        @{ Port = 4000; Service = "REST API" }
        @{ Port = 1618; Service = "Dashboard" }
        @{ Port = 9100; Service = "Prometheus Metrics" }
        @{ Port = 5432; Service = "PostgreSQL" }
        @{ Port = $coinInfo.RpcPort; Service = "$Coin RPC" }
        @{ Port = $coinInfo.P2pPort; Service = "$Coin P2P" }
    )

    foreach ($portInfo in $ports) {
        try {
            $connection = Test-NetConnection -ComputerName localhost -Port $portInfo.Port -WarningAction SilentlyContinue
            if ($connection.TcpTestSucceeded) {
                Add-TestResult -Category "Network" -Test "$($portInfo.Service) (port $($portInfo.Port))" `
                    -Status "PASS"
            } else {
                Add-TestResult -Category "Network" -Test "$($portInfo.Service) (port $($portInfo.Port))" `
                    -Status "FAIL" -Message "Connection refused"
            }
        } catch {
            Add-TestResult -Category "Network" -Test "$($portInfo.Service) (port $($portInfo.Port))" `
                -Status "FAIL" -Message $_.Exception.Message
        }
    }

    # Firewall rules check
    Write-Host ""
    Write-Host "    Firewall Rules:" -ForegroundColor DarkGray

    try {
        $fwRules = Get-NetFirewallRule -DisplayName "Spiral Pool*" -ErrorAction SilentlyContinue
        if ($fwRules) {
            $ruleCount = ($fwRules | Measure-Object).Count
            $publicRules = $fwRules | Where-Object {
                $profile = (Get-NetFirewallProfile -AssociatedNetFirewallRule $_ -ErrorAction SilentlyContinue).Name
                $profile -contains "Public"
            }
            if ($publicRules) {
                Add-TestResult -Category "Network" -Test "Firewall rules (security)" `
                    -Status "WARN" -Message "$ruleCount rules, some allow Public network"
            } else {
                Add-TestResult -Category "Network" -Test "Firewall rules (security)" `
                    -Status "PASS" -Message "$ruleCount rules, Private/Domain only"
            }
        } else {
            Add-TestResult -Category "Network" -Test "Firewall rules (security)" `
                -Status "WARN" -Message "No Spiral Pool firewall rules found"
        }
    } catch {
        Add-TestResult -Category "Network" -Test "Firewall rules (security)" `
            -Status "SKIP"
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# TEST CATEGORY: HEALTH (dynamic per coin)
# ═══════════════════════════════════════════════════════════════════════════════

function Test-Health {
    Write-TestSection "SERVICE HEALTH ($Coin)"

    # Test API endpoint
    try {
        $response = Invoke-WebRequest -Uri "http://localhost:4000/api/pools" -UseBasicParsing -TimeoutSec 10
        if ($response.StatusCode -eq 200) {
            Add-TestResult -Category "Health" -Test "Pool API /api/pools" `
                -Status "PASS" -Message "HTTP 200"
        } else {
            Add-TestResult -Category "Health" -Test "Pool API /api/pools" `
                -Status "WARN" -Message "HTTP $($response.StatusCode)"
        }
    } catch {
        Add-TestResult -Category "Health" -Test "Pool API /api/pools" `
            -Status "FAIL" -Message "Connection failed"
    }

    # Test Dashboard
    try {
        $response = Invoke-WebRequest -Uri "http://localhost:1618/api/health/live" -UseBasicParsing -TimeoutSec 10
        if ($response.StatusCode -eq 200) {
            Add-TestResult -Category "Health" -Test "Dashboard health endpoint" `
                -Status "PASS" -Message "HTTP 200"
        } else {
            Add-TestResult -Category "Health" -Test "Dashboard health endpoint" `
                -Status "WARN" -Message "HTTP $($response.StatusCode)"
        }
    } catch {
        Add-TestResult -Category "Health" -Test "Dashboard health endpoint" `
            -Status "FAIL" -Message "Connection failed"
    }

    # Test Prometheus metrics
    try {
        $response = Invoke-WebRequest -Uri "http://localhost:9100/metrics" -UseBasicParsing -TimeoutSec 10
        if ($response.StatusCode -eq 200 -and $response.Content -match "spiralpool") {
            Add-TestResult -Category "Health" -Test "Prometheus metrics" `
                -Status "PASS" -Message "Metrics available"
        } elseif ($response.StatusCode -eq 200) {
            Add-TestResult -Category "Health" -Test "Prometheus metrics" `
                -Status "WARN" -Message "Endpoint up, no pool metrics yet"
        } else {
            Add-TestResult -Category "Health" -Test "Prometheus metrics" `
                -Status "WARN" -Message "HTTP $($response.StatusCode)"
        }
    } catch {
        Add-TestResult -Category "Health" -Test "Prometheus metrics" `
            -Status "FAIL" -Message "Connection failed"
    }

    # Check blockchain sync progress (dynamic per coin)
    $containerName = $coinInfo.ContainerName
    $cliName = $coinInfo.CliName
    try {
        $syncInfo = docker exec $containerName $cliName getblockchaininfo 2>&1 | ConvertFrom-Json
        $progress = [math]::Round($syncInfo.verificationprogress * 100, 2)
        if ($progress -ge 99.9) {
            Add-TestResult -Category "Health" -Test "$Coin blockchain sync" `
                -Status "PASS" -Message "Fully synced ($progress%)"
        } elseif ($progress -gt 0) {
            Add-TestResult -Category "Health" -Test "$Coin blockchain sync" `
                -Status "WARN" -Message "Syncing: $progress%"
        } else {
            Add-TestResult -Category "Health" -Test "$Coin blockchain sync" `
                -Status "WARN" -Message "Not started or very early"
        }
    } catch {
        Add-TestResult -Category "Health" -Test "$Coin blockchain sync" `
            -Status "SKIP" -Message "Could not query blockchain"
    }

    # Check scheduled tasks
    Write-Host ""
    Write-Host "    Scheduled Tasks:" -ForegroundColor DarkGray

    $tasks = @("SpiralPoolHealthCheck", "SpiralPoolStartup", "SpiralPoolWSLPorts")
    foreach ($task in $tasks) {
        try {
            $taskInfo = Get-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue
            if ($taskInfo) {
                Add-TestResult -Category "Health" -Test "Scheduled task: $task" `
                    -Status "PASS" -Message "State: $($taskInfo.State)"
            } else {
                Add-TestResult -Category "Health" -Test "Scheduled task: $task" `
                    -Status "SKIP" -Message "Not found"
            }
        } catch {
            Add-TestResult -Category "Health" -Test "Scheduled task: $task" `
                -Status "SKIP"
        }
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# TEST CATEGORY: PERFORMANCE
# ═══════════════════════════════════════════════════════════════════════════════

function Test-Performance {
    Write-TestSection "PERFORMANCE BASELINES ($Coin)"

    Write-Host "    Note: Performance tests establish baselines for Windows Docker deployment." -ForegroundColor DarkGray
    Write-Host "    These values may differ significantly from native Linux performance." -ForegroundColor DarkGray
    Write-Host ""

    # API response time
    try {
        $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
        $response = Invoke-WebRequest -Uri "http://localhost:4000/api/pools" -UseBasicParsing -TimeoutSec 10
        $stopwatch.Stop()
        $responseTime = $stopwatch.ElapsedMilliseconds

        if ($responseTime -lt 100) {
            Add-TestResult -Category "Performance" -Test "API response time" `
                -Status "PASS" -Message "${responseTime}ms (excellent)"
        } elseif ($responseTime -lt 500) {
            Add-TestResult -Category "Performance" -Test "API response time" `
                -Status "PASS" -Message "${responseTime}ms (good)"
        } elseif ($responseTime -lt 2000) {
            Add-TestResult -Category "Performance" -Test "API response time" `
                -Status "WARN" -Message "${responseTime}ms (slow)"
        } else {
            Add-TestResult -Category "Performance" -Test "API response time" `
                -Status "FAIL" -Message "${responseTime}ms (very slow)"
        }
    } catch {
        Add-TestResult -Category "Performance" -Test "API response time" `
            -Status "SKIP" -Message "Could not measure"
    }

    # Container memory usage (dynamic per coin)
    $containerName = $coinInfo.ContainerName
    try {
        $containers = @($containerName, "spiralpool-stratum", "spiralpool-postgres")
        $totalMemMB = 0
        foreach ($container in $containers) {
            $stats = docker stats $container --no-stream --format "{{.MemUsage}}" 2>&1
            if ($stats -match "(\d+\.?\d*)(MiB|GiB)") {
                $value = [double]$Matches[1]
                if ($Matches[2] -eq "GiB") { $value *= 1024 }
                $totalMemMB += $value
            }
        }
        $totalMemGB = [math]::Round($totalMemMB / 1024, 2)
        Add-TestResult -Category "Performance" -Test "Container memory usage" `
            -Status "PASS" -Message "${totalMemGB} GB total"
    } catch {
        Add-TestResult -Category "Performance" -Test "Container memory usage" `
            -Status "SKIP"
    }

    # Disk I/O baseline (informational)
    $volumeName = "spiralpool-$($coinInfo.Container)-data"
    try {
        $volumePath = docker volume inspect $volumeName --format '{{.Mountpoint}}' 2>&1
        Add-TestResult -Category "Performance" -Test "Volume mount point" `
            -Status "PASS" -Message "WSL2 managed volume"
    } catch {
        Add-TestResult -Category "Performance" -Test "Volume mount point" `
            -Status "SKIP"
    }
}

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN EXECUTION
# ═══════════════════════════════════════════════════════════════════════════════

Write-TestHeader "SPIRAL POOL - WINDOWS DEPLOYMENT TEST MATRIX"

Write-Host "  Test Categories: $Category" -ForegroundColor Cyan
Write-Host "  Coin: $Coin (profile: $($coinInfo.Profile))" -ForegroundColor Cyan
Write-Host "  Installation Directory: $InstallDir" -ForegroundColor Cyan
Write-Host "  Timestamp: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')" -ForegroundColor Cyan

# Run tests based on category
switch ($Category) {
    "All" {
        Test-Prerequisites
        Test-Docker
        Test-Containers
        Test-Network
        Test-Health
        Test-Performance
    }
    "Prerequisites" { Test-Prerequisites }
    "Docker" { Test-Docker }
    "Containers" { Test-Containers }
    "Network" { Test-Network }
    "Health" { Test-Health }
    "Performance" { Test-Performance }
}

# ═══════════════════════════════════════════════════════════════════════════════
# SUMMARY
# ═══════════════════════════════════════════════════════════════════════════════

Write-Host ""
Write-Host "╔════════════════════════════════════════════════════════════════════════════╗" -ForegroundColor Cyan
Write-Host "║  $("TEST SUMMARY ($Coin)".PadRight(74))║" -ForegroundColor Cyan
Write-Host "╚════════════════════════════════════════════════════════════════════════════╝" -ForegroundColor Cyan
Write-Host ""

$totalTests = $Script:PassCount + $Script:FailCount + $Script:WarnCount + $Script:SkipCount

Write-Host "    Total Tests:  $totalTests" -ForegroundColor White
Write-Host "    Passed:       " -NoNewline; Write-Host "$($Script:PassCount)" -ForegroundColor Green
Write-Host "    Failed:       " -NoNewline; Write-Host "$($Script:FailCount)" -ForegroundColor Red
Write-Host "    Warnings:     " -NoNewline; Write-Host "$($Script:WarnCount)" -ForegroundColor Yellow
Write-Host "    Skipped:      " -NoNewline; Write-Host "$($Script:SkipCount)" -ForegroundColor DarkGray
Write-Host ""

# Overall verdict
if ($Script:FailCount -eq 0 -and $Script:WarnCount -eq 0) {
    Write-Host "    VERDICT: " -NoNewline
    Write-Host "ALL TESTS PASSED" -ForegroundColor Green
} elseif ($Script:FailCount -eq 0) {
    Write-Host "    VERDICT: " -NoNewline
    Write-Host "PASSED WITH WARNINGS" -ForegroundColor Yellow
} else {
    Write-Host "    VERDICT: " -NoNewline
    Write-Host "TESTS FAILED" -ForegroundColor Red
}

Write-Host ""
Write-Host "    Note: Windows Docker deployment is EXPERIMENTAL." -ForegroundColor DarkGray
Write-Host "    Some warnings are expected due to untested scenarios." -ForegroundColor DarkGray
Write-Host ""

# Export report if requested
if ($OutputReport) {
    $report = @{
        Timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
        Platform = "Windows"
        Coin = $Coin
        Profile = $coinInfo.Profile
        Category = $Category
        InstallDir = $InstallDir
        Summary = @{
            Total = $totalTests
            Passed = $Script:PassCount
            Failed = $Script:FailCount
            Warnings = $Script:WarnCount
            Skipped = $Script:SkipCount
        }
        Results = $Script:TestResults
    }

    $report | ConvertTo-Json -Depth 4 | Set-Content -Path $OutputReport -Encoding UTF8
    Write-Host "    Report saved to: $OutputReport" -ForegroundColor Cyan
}

# Return exit code based on failures
if ($Script:FailCount -gt 0) {
    exit 1
} else {
    exit 0
}
