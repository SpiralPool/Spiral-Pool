# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
#
# wsl2-stratum-proxy.ps1
# WSL2 Port Proxy Manager for Spiral Pool
#
# Automatically routes ASIC/miner traffic from the Windows LAN IP into
# the WSL2-hosted Spiral Pool stratum server using netsh portproxy.
#
# - Handles WSL2 installation if not present
# - Auto-detects Windows LAN IP and WSL2 IP
# - Adds portproxy + firewall rules for the selected coin
# - Removes portproxy rules cleanly when you press Ctrl+C (window X-button does not clean up)

$ErrorActionPreference = 'Stop'

# ─── Spiral Pool coin port table ────────────────────────────────────────────
# Ports must match the Spiral Pool installer. Do not change these.
$COIN_PORTS = @(
    [pscustomobject]@{ Num=1;  Code='DGB';       Name='DigiByte (SHA-256d)';        V1=3333;  V2=3334;  TLS=3335  },
    [pscustomobject]@{ Num=2;  Code='DGB-SCRYPT'; Name='DigiByte (Scrypt)';         V1=3336;  V2=3337;  TLS=3338  },
    [pscustomobject]@{ Num=3;  Code='BTC';       Name='Bitcoin (SHA-256d)';         V1=4333;  V2=4334;  TLS=4335  },
    [pscustomobject]@{ Num=4;  Code='BCH';       Name='Bitcoin Cash (SHA-256d)';    V1=5333;  V2=5334;  TLS=5335  },
    [pscustomobject]@{ Num=5;  Code='BC2';       Name='Bitcoin II (SHA-256d)';      V1=6333;  V2=6334;  TLS=6335  },
    [pscustomobject]@{ Num=6;  Code='LTC';       Name='Litecoin (Scrypt)';          V1=7333;  V2=7334;  TLS=7335  },
    [pscustomobject]@{ Num=7;  Code='DOGE';      Name='Dogecoin (Scrypt)';          V1=8335;  V2=8337;  TLS=8342  },
    [pscustomobject]@{ Num=8;  Code='PEP';       Name='Pepecoin (Scrypt)';          V1=10335; V2=10336; TLS=10337 },
    [pscustomobject]@{ Num=9;  Code='CAT';       Name='Catcoin (Scrypt)';           V1=12335; V2=12336; TLS=12337 },
    [pscustomobject]@{ Num=10; Code='NMC';       Name='Namecoin (SHA-256d)';        V1=14335; V2=14336; TLS=14337 },
    [pscustomobject]@{ Num=11; Code='SYS';       Name='Syscoin (SHA-256d)';         V1=15335; V2=15336; TLS=15337 },
    [pscustomobject]@{ Num=12; Code='XMY';       Name='Myriadcoin (SHA-256d)';      V1=17335; V2=17336; TLS=17337 },
    [pscustomobject]@{ Num=13; Code='FBTC';      Name='Fractal Bitcoin (SHA-256d)'; V1=18335; V2=18336; TLS=18337 },
    [pscustomobject]@{ Num=14; Code='QBX';       Name='Q-BitX (SHA-256d)';          V1=20335; V2=20336; TLS=20337 }
)

# ─── Banner ──────────────────────────────────────────────────────────────────
Clear-Host
Write-Host ""
Write-Host "  ╔══════════════════════════════════════════════════════════════╗" -ForegroundColor Cyan
Write-Host "  ║     SPIRAL POOL  -  WSL2 Port Proxy  [EXPERIMENTAL]        ║" -ForegroundColor Cyan
Write-Host "  ║   Routes ASIC/miner traffic into your WSL2 stratum server   ║" -ForegroundColor Cyan
Write-Host "  ╚══════════════════════════════════════════════════════════════╝" -ForegroundColor Cyan
Write-Host ""

# ─── Elevation check ─────────────────────────────────────────────────────────
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host "  [!] This script must run as Administrator." -ForegroundColor Red
    Write-Host "      Use start-wsl2-proxy.bat which handles this automatically." -ForegroundColor Yellow
    Write-Host ""
    Read-Host "  Press Enter to exit"
    exit 1
}

# ─── WSL2 limitations warning ────────────────────────────────────────────────
Write-Host "  ┌─────────────────────────────────────────────────────────────┐" -ForegroundColor Yellow
Write-Host "  │  ⚠  WSL2 LIMITATIONS — READ BEFORE CONTINUING              │" -ForegroundColor Yellow
Write-Host "  └─────────────────────────────────────────────────────────────┘" -ForegroundColor Yellow
Write-Host ""
Write-Host "  Spiral Pool runs inside WSL2, but has important differences" -ForegroundColor White
Write-Host "  from a native Ubuntu installation:" -ForegroundColor White
Write-Host ""
Write-Host "  systemd reliability" -ForegroundColor White
Write-Host "    Spiral Pool relies on systemd services (spiralstratum, spiralsentinel," -ForegroundColor Gray
Write-Host "    etc.). WSL2 systemd support exists (Windows 11 22H2+) but is not" -ForegroundColor Gray
Write-Host "    fully reliable. Services may fail to start or not restart on failure." -ForegroundColor Gray
Write-Host ""
Write-Host "  No auto-start on Windows boot" -ForegroundColor White
Write-Host "    WSL2 does not launch when Windows starts. Your stratum goes offline" -ForegroundColor Gray
Write-Host "    on every reboot until you manually open Ubuntu. This script can set" -ForegroundColor Gray
Write-Host "    up a Task Scheduler entry to handle this automatically." -ForegroundColor Gray
Write-Host ""
Write-Host "  Windows can terminate WSL2 without warning" -ForegroundColor White
Write-Host "    Windows Updates, sleep, hibernate, and memory pressure can all kill" -ForegroundColor Gray
Write-Host "    the WSL2 instance mid-operation. On native Linux this cannot happen." -ForegroundColor Gray
Write-Host ""
Write-Host "  I/O performance" -ForegroundColor White
Write-Host "    Blockchain sync is 2-4x slower due to the virtual disk (.vhdx)." -ForegroundColor Gray
Write-Host "    Large chains (BTC: ~600 GB, DGB: ~60 GB) take significantly longer." -ForegroundColor Gray
Write-Host "    PostgreSQL write performance under mining load is also reduced." -ForegroundColor Gray
Write-Host ""
Write-Host "  Memory cap" -ForegroundColor White
Write-Host "    WSL2 imposes a memory cap on the Linux VM. Multi-coin setups" -ForegroundColor Gray
Write-Host "    or memory-heavy nodes can hit this limit. Configurable via" -ForegroundColor Gray
Write-Host "    %USERPROFILE%\.wslconfig  (see: [memory] section)." -ForegroundColor Gray
Write-Host ""
Write-Host "  Two firewalls apply — WSL2 (UFW) and Windows Firewall" -ForegroundColor White
Write-Host "    UFW inside WSL2 controls inbound access to stratum services." -ForegroundColor Gray
Write-Host "    install.sh configures UFW correctly. However, Windows Firewall" -ForegroundColor Gray
Write-Host "    also controls what reaches WSL2 from outside your PC — install.sh" -ForegroundColor Gray
Write-Host "    does not touch Windows Firewall. This script adds those rules." -ForegroundColor Gray
Write-Host ""
Write-Host "  HA is non-functional" -ForegroundColor White
Write-Host "    keepalived (VIP), etcd, and Patroni multi-node HA require network" -ForegroundColor Gray
Write-Host "    features WSL2 cannot provide. Full HA requires native Linux." -ForegroundColor Gray
Write-Host ""
Write-Host "  Clock drift" -ForegroundColor White
Write-Host "    WSL2 clocks can drift after Windows sleep/hibernate, causing share" -ForegroundColor Gray
Write-Host "    timestamp issues until WSL2 re-syncs with the Windows clock." -ForegroundColor Gray
Write-Host ""
Write-Host "  Mirrored networking (eliminates portproxy)" -ForegroundColor White
Write-Host "    Windows 11 23H2+ (Build 22631+) supports networkingMode=mirrored in" -ForegroundColor Gray
Write-Host "    %USERPROFILE%\.wslconfig. This shares the Windows network stack with" -ForegroundColor Gray
Write-Host "    WSL2 so miners can reach stratum directly on your Windows LAN IP." -ForegroundColor Gray
Write-Host "    No portproxy or bat file needed. To enable:" -ForegroundColor Gray
Write-Host "        [wsl2]" -ForegroundColor DarkGray
Write-Host "        networkingMode=mirrored" -ForegroundColor DarkGray
Write-Host "    Then restart WSL2: wsl --shutdown" -ForegroundColor Gray
Write-Host ""
Write-Host "  ─────────────────────────────────────────────────────────────" -ForegroundColor DarkGray
Write-Host "  TL;DR  WSL2 support is EXPERIMENTAL and not recommended for" -ForegroundColor Yellow
Write-Host "         production. Fine for evaluation and development only." -ForegroundColor Yellow
Write-Host "         For 24/7 production mining, use native Ubuntu." -ForegroundColor Cyan
Write-Host "  ─────────────────────────────────────────────────────────────" -ForegroundColor DarkGray
Write-Host ""
$ack = Read-Host "  Understood — continue? [Y/N]"
if ($ack -notmatch '^[Yy]') {
    Write-Host ""
    Write-Host "  Exited. No changes were made." -ForegroundColor Gray
    Write-Host ""
    exit 0
}
Write-Host ""

# ─── WSL2 installation check ─────────────────────────────────────────────────
Write-Host "  Checking WSL2..." -ForegroundColor Cyan

$wslReady = $false
$selectedDistro = $null
try {
    # wsl -l -q outputs UTF-16 LE; set encoding to prevent garbled output in PS5
    $origEncoding = [Console]::OutputEncoding
    [Console]::OutputEncoding = [System.Text.Encoding]::Unicode
    $wslList = & wsl -l -q 2>&1
    [Console]::OutputEncoding = $origEncoding
    # Filter out blank lines and the "Windows Subsystem for Linux Distributions:" header
    $distros = @($wslList | Where-Object { $_ -match '\S' -and $_ -notmatch 'Windows Subsystem' } |
                 ForEach-Object { $_.Trim().TrimEnd('*') })
    if ($distros.Count -gt 0) {
        $wslReady = $true
        if ($distros.Count -eq 1) {
            $selectedDistro = $distros[0]
            Write-Host "  WSL2 ready. Distro: $selectedDistro" -ForegroundColor Green
        } else {
            Write-Host "  WSL2 ready. Multiple distros found:" -ForegroundColor Green
            Write-Host ""
            for ($i = 0; $i -lt $distros.Count; $i++) {
                Write-Host ("  [{0}]  {1}" -f ($i + 1), $distros[$i])
            }
            Write-Host ""
            $dpick = Read-Host "  Which distro has Spiral Pool installed? [1]"
            if ([string]::IsNullOrEmpty($dpick)) { $dpick = '1' }
            $didx = ([int]$dpick) - 1
            if ($didx -lt 0 -or $didx -ge $distros.Count) {
                Write-Host "  Invalid selection — using first distro." -ForegroundColor Yellow
                $didx = 0
            }
            $selectedDistro = $distros[$didx]
            Write-Host "  Using distro: $selectedDistro" -ForegroundColor Cyan
        }
    }
} catch {
    Write-Host "  [!] WSL2 detection error: $_" -ForegroundColor Yellow
}

if (-not $wslReady) {
    Write-Host ""
    Write-Host "  [!] WSL2 is not installed or has no Linux distribution." -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  Spiral Pool runs inside WSL2 (Ubuntu on Windows)." -ForegroundColor White
    Write-Host "  Installing WSL2 will also install Ubuntu automatically." -ForegroundColor White
    Write-Host ""
    $doInstall = Read-Host "  Install WSL2 + Ubuntu now? [Y/N]"
    if ($doInstall -match '^[Yy]') {
        Write-Host ""
        Write-Host "  Running: wsl --install -d Ubuntu" -ForegroundColor Cyan
        Write-Host ""
        & wsl --install -d Ubuntu
        Write-Host ""
        Write-Host "  ────────────────────────────────────────────────────────────" -ForegroundColor Yellow
        Write-Host "  NEXT STEPS:" -ForegroundColor Yellow
        Write-Host "  1. Restart your computer when prompted." -ForegroundColor White
        Write-Host "  2. After restart, open Ubuntu from the Start menu." -ForegroundColor White
        Write-Host "  3. Create your Linux username and password." -ForegroundColor White
        Write-Host "  4. Clone and run the Spiral Pool installer inside Ubuntu:" -ForegroundColor White
        Write-Host "       git clone --depth 1 https://github.com/SpiralPool/Spiral-Pool.git" -ForegroundColor Gray
        Write-Host "       cd Spiral-Pool && ./install.sh" -ForegroundColor Gray
        Write-Host "  5. Then run start-wsl2-proxy.bat again to set up routing." -ForegroundColor White
        Write-Host "  ────────────────────────────────────────────────────────────" -ForegroundColor Yellow
    } else {
        Write-Host ""
        Write-Host "  WSL2 is required to run Spiral Pool on Windows." -ForegroundColor Red
        Write-Host "  Docs: https://learn.microsoft.com/en-us/windows/wsl/install" -ForegroundColor Gray
    }
    Write-Host ""
    Read-Host "  Press Enter to exit"
    exit 0
}

# ─── Detect WSL2 IP ──────────────────────────────────────────────────────────
Write-Host ""
Write-Host "  Detecting WSL2 IP address..." -ForegroundColor Cyan

$wslIP = $null
try {
    $wslArgs = if ($selectedDistro) { @('-d', $selectedDistro, '--exec', 'hostname', '-I') } else { @('--exec', 'hostname', '-I') }
    $raw = (& wsl @wslArgs).Trim()
    $wslIP = ($raw.Split(' ') | Where-Object { $_ -match '^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$' } | Select-Object -First 1)
} catch {
    Write-Host "  [!] WSL2 error: $_" -ForegroundColor Yellow
}

if (-not $wslIP) {
    Write-Host "  [!] Could not auto-detect WSL2 IP. Is WSL2 running?" -ForegroundColor Yellow
    Write-Host "      Start Ubuntu from the Start menu, then try again, OR enter the IP manually." -ForegroundColor White
    Write-Host ""
    $wslIP = Read-Host "  Enter WSL2 IP (e.g. 172.18.x.x)"
    if (-not $wslIP) { Write-Host "  Aborted." -ForegroundColor Red; exit 1 }
}

Write-Host "  WSL2 IP : $wslIP" -ForegroundColor White

# ─── Detect Windows LAN IP ───────────────────────────────────────────────────
Write-Host ""
Write-Host "  Detecting Windows LAN IP..." -ForegroundColor Cyan

$candidates = Get-NetIPAddress -AddressFamily IPv4 | Where-Object {
    $_.IPAddress -notmatch '^127\.'       -and   # loopback
    $_.IPAddress -notmatch '^169\.254\.'  -and   # link-local
    $_.IPAddress -notmatch '^172\.(1[6-9]|2[0-9]|3[01])\.'  -and  # WSL/Docker bridge range
    $_.IPAddress -ne '0.0.0.0'           -and
    $_.PrefixOrigin -ne 'WellKnown'
} | Sort-Object InterfaceMetric

$lanIP = $null

if ($candidates.Count -eq 0) {
    Write-Host "  [!] No LAN IP detected automatically." -ForegroundColor Yellow
    $lanIP = Read-Host "  Enter your Windows LAN IP (e.g. 192.168.1.161)"
    if (-not $lanIP) { Write-Host "  Aborted." -ForegroundColor Red; exit 1 }
} elseif ($candidates.Count -eq 1) {
    $lanIP = $candidates[0].IPAddress
    Write-Host "  LAN IP  : $lanIP" -ForegroundColor White
} else {
    Write-Host ""
    Write-Host "  Multiple interfaces found. Select your LAN adapter:" -ForegroundColor White
    Write-Host ""
    for ($i = 0; $i -lt $candidates.Count; $i++) {
        $adapterName = (Get-NetAdapter | Where-Object { $_.ifIndex -eq $candidates[$i].InterfaceIndex } | Select-Object -First 1).Name
        Write-Host ("  [{0}] {1,-16}  {2}" -f ($i + 1), $candidates[$i].IPAddress, $adapterName)
    }
    Write-Host ""
    $pick = Read-Host "  Select [1]"
    if ([string]::IsNullOrEmpty($pick)) { $pick = '1' }
    $idx = ([int]$pick) - 1
    if ($idx -lt 0 -or $idx -ge $candidates.Count) {
        Write-Host "  Invalid selection." -ForegroundColor Red; exit 1
    }
    $lanIP = $candidates[$idx].IPAddress
}

# ─── Confirm or override detected addresses ───────────────────────────────────
Write-Host ""
Write-Host "  ──────────────────────────────────────────────────" -ForegroundColor Cyan
Write-Host ("  Windows LAN IP  :  {0}" -f $lanIP) -ForegroundColor White
Write-Host ("  WSL2 IP         :  {0}" -f $wslIP) -ForegroundColor White
Write-Host "  ──────────────────────────────────────────────────" -ForegroundColor Cyan
Write-Host ""
$ok = Read-Host "  Use these addresses? [Y/N]"
if ($ok -notmatch '^[Yy]') {
    $lanIP = Read-Host "  Windows LAN IP"
    $wslIP = Read-Host "  WSL2 IP"
}

# ─── Mirrored networking detection ────────────────────────────────
if ($wslIP -eq $lanIP) {
    Write-Host ""
    Write-Host "  ══════════════════════════════════════════════════" -ForegroundColor Yellow
    Write-Host "  MIRRORED NETWORKING DETECTED" -ForegroundColor Yellow
    Write-Host "  ══════════════════════════════════════════════════" -ForegroundColor Yellow
    Write-Host ""
    Write-Host ("  WSL2 IP ({0}) matches your Windows LAN IP." -f $wslIP) -ForegroundColor White
    Write-Host "  WSL2 is using networkingMode=mirrored, which shares the" -ForegroundColor White
    Write-Host "  Windows network stack directly with WSL2." -ForegroundColor White
    Write-Host ""
    Write-Host "  portproxy rules are NOT needed in mirrored mode." -ForegroundColor Cyan
    Write-Host "  ASIC miners can connect directly to your Windows LAN IP:" -ForegroundColor White
    Write-Host ("    {0}:<stratum_port>" -f $lanIP) -ForegroundColor Green
    Write-Host ""
    Write-Host "  Adding portproxy rules in mirrored mode would route traffic" -ForegroundColor Yellow
    Write-Host "  back to the same machine and break connectivity." -ForegroundColor Yellow
    Write-Host ""
    $cont = Read-Host "  Continue anyway (not recommended)? [y/N]"
    if ($cont -notmatch '^[Yy]') {
        Write-Host ""
        Write-Host "  Exiting. No rules applied." -ForegroundColor Green
        Write-Host "  Your miners can connect to $lanIP:<stratum_port> directly." -ForegroundColor Cyan
        Read-Host "  Press Enter to exit"
        exit 0
    }
    Write-Host ""
    Write-Host "  Continuing at your request. Rules will target $wslIP." -ForegroundColor Yellow
}


# ─── Coin / port selection ────────────────────────────────────────────────────
Write-Host ""
Write-Host "  ──────────────────────────────────────────────────" -ForegroundColor Cyan
Write-Host "  SELECT COIN" -ForegroundColor White
Write-Host "  ──────────────────────────────────────────────────" -ForegroundColor Cyan
Write-Host ""
foreach ($c in $COIN_PORTS) {
    Write-Host ("  [{0,2}]  {1,-10}  {2,-34}  V1:{3,-6}  V2:{4,-6}  TLS:{5}" -f $c.Num, $c.Code, $c.Name, $c.V1, $c.V2, $c.TLS)
}
Write-Host "  [ALL]  Forward all coins"
Write-Host "  [ C ]  Custom port"
Write-Host ""
$sel = Read-Host "  Enter number, ALL, or C"

$selectedPorts = @()
$selectedLabel = ''

if ($sel -match '^[Cc]$') {
    $customV1  = Read-Host "  Enter stratum V1 port"
    $customV2  = Read-Host "  Enter stratum V2 port (leave blank to skip)"
    $customTLS = Read-Host "  Enter stratum TLS port (leave blank to skip)"
    $selectedPorts += [int]$customV1
    if ($customV2)  { $selectedPorts += [int]$customV2 }
    if ($customTLS) { $selectedPorts += [int]$customTLS }
    $selectedLabel = "Custom port $customV1"
} elseif ($sel -match '^[Aa][Ll][Ll]$') {
    foreach ($c in $COIN_PORTS) {
        $selectedPorts += $c.V1
        $selectedPorts += $c.V2
        $selectedPorts += $c.TLS
    }
    $selectedLabel = 'All coins'
} else {
    $num = [int]$sel
    $coin = $COIN_PORTS | Where-Object { $_.Num -eq $num } | Select-Object -First 1
    if (-not $coin) {
        Write-Host "  [!] Invalid selection." -ForegroundColor Red
        Read-Host "  Press Enter to exit"
        exit 1
    }
    $selectedPorts += $coin.V1
    $selectedPorts += $coin.V2
    $selectedPorts += $coin.TLS
    $selectedLabel = "$($coin.Code) - $($coin.Name)"
}

# ─── Add portproxy rules ──────────────────────────────────────────────────────
Write-Host ""
Write-Host "  Adding portproxy rules..." -ForegroundColor Cyan

$addedPorts = @()
foreach ($port in $selectedPorts) {
    $result = netsh interface portproxy add v4tov4 `
        listenport=$port listenaddress=0.0.0.0 `
        connectport=$port connectaddress=$wslIP 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host ("  [!] netsh failed for port {0}: $result" -f $port) -ForegroundColor Red
    } else {
        $addedPorts += $port
        Write-Host ("  +  0.0.0.0:{0,-6}  ->  {1}:{2}" -f $port, $wslIP, $port) -ForegroundColor Green
    }
}

# ─── Add Windows Firewall rules (inbound) ────────────────────────────────────
Write-Host ""
Write-Host "  Checking Windows Firewall rules..." -ForegroundColor Cyan

$firewallFailed = $false
foreach ($port in $selectedPorts) {
    $ruleName = "SpiralPool-Stratum-$port"
    $existing = Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue
    if (-not $existing) {
        try {
            New-NetFirewallRule -DisplayName $ruleName `
                -Direction Inbound -Protocol TCP -LocalPort $port `
                -Action Allow -Profile Any | Out-Null
            Write-Host "  +  Firewall: allowed TCP $port (inbound)" -ForegroundColor Green
        } catch {
            Write-Host ("  [!] Firewall rule failed for port {0}: {1}" -f $port, $_) -ForegroundColor Yellow
            Write-Host "      Portproxy rules are still active. Add the rule manually if needed." -ForegroundColor Gray
            $firewallFailed = $true
        }
    } else {
        Write-Host "  o  Firewall: TCP $port already allowed" -ForegroundColor Gray
    }
}
if ($firewallFailed) {
    Write-Host ""
    Write-Host "  [!] Some firewall rules could not be created." -ForegroundColor Yellow
    Write-Host "      Miners on other PCs may be blocked by Windows Firewall." -ForegroundColor Gray
    Write-Host "      Add rules manually: Windows Defender Firewall > Inbound Rules > New Rule" -ForegroundColor Gray
}

# ─── Guard: exit if no rules were applied ────────────────────────────────────
if ($addedPorts.Count -eq 0) {
    Write-Host ""
    Write-Host "  [!] All portproxy rules failed to apply." -ForegroundColor Red
    Write-Host "  Check that WSL2 is running and that the WSL2 IP is reachable." -ForegroundColor White
    Write-Host "  Verify WSL2 IP with: wsl hostname -I" -ForegroundColor Gray
    Write-Host ""
    Read-Host "  Press Enter to exit"
    exit 1
}

# ─── Running status ───────────────────────────────────────────────────────────
$primaryPort = $addedPorts[0]

Write-Host ""
Write-Host "  ╔══════════════════════════════════════════════════════════════╗" -ForegroundColor Green
Write-Host "  ║  SPIRAL POOL PROXY  -  ACTIVE                               ║" -ForegroundColor Green
Write-Host "  ╠══════════════════════════════════════════════════════════════╣" -ForegroundColor Green
Write-Host ("  ║  Coin     : {0,-50}║" -f $selectedLabel) -ForegroundColor White
Write-Host ("  ║  Route    : {0}:{1}  ->  {2}:{1,-13}║" -f $lanIP, $primaryPort, $wslIP) -ForegroundColor White
Write-Host "  ║                                                              ║" -ForegroundColor White
Write-Host ("  ║  Miner stratum host  :  {0,-38}║" -f $lanIP) -ForegroundColor White
Write-Host ("  ║  Miner stratum port  :  {0,-38}║" -f $primaryPort) -ForegroundColor White
Write-Host "  ║                                                              ║" -ForegroundColor White
Write-Host "  ║  Press Ctrl+C to stop and remove portproxy rules            ║" -ForegroundColor Yellow
Write-Host "  ╚══════════════════════════════════════════════════════════════╝" -ForegroundColor Green
Write-Host ""

# ─── Auto-start on Windows boot ──────────────────────────────────────────────
Write-Host ""
Write-Host "  ─────────────────────────────────────────────────────────────" -ForegroundColor Cyan
Write-Host "  AUTO-START ON WINDOWS BOOT" -ForegroundColor White
Write-Host "  ─────────────────────────────────────────────────────────────" -ForegroundColor Cyan
Write-Host ""
Write-Host "  WSL2 does not start automatically when Windows boots." -ForegroundColor White
Write-Host "  Without auto-start, your stratum goes offline on every restart" -ForegroundColor White
Write-Host "  until you manually open Ubuntu." -ForegroundColor White
Write-Host ""
Write-Host "  A Windows Task Scheduler entry can start WSL2 and your Spiral" -ForegroundColor White
Write-Host "  Pool services automatically at logon." -ForegroundColor White
Write-Host ""
$setupAutoStart = Read-Host "  Set up auto-start via Task Scheduler? [Y/N]"
if ($setupAutoStart -match '^[Yy]') {
    # Verify systemd is enabled in WSL2 — the task uses systemctl
    $systemdEnabled = $false
    try {
        $wslConfArgs = if ($selectedDistro) { @('-d', $selectedDistro, '--user', 'root', '--exec', 'cat', '/etc/wsl.conf') }
                       else                 { @('--user', 'root', '--exec', 'cat', '/etc/wsl.conf') }
        $wslConf = (& wsl @wslConfArgs 2>&1) -join "`n"
        $systemdEnabled = ($wslConf -match 'systemd\s*=\s*true')
    } catch {}
    if (-not $systemdEnabled) {
        Write-Host ""
        Write-Host "  [!] systemd does not appear to be enabled in WSL2." -ForegroundColor Yellow
        Write-Host "  The auto-start task uses systemctl and will fail without it." -ForegroundColor White
        Write-Host "  Enable it by adding this to /etc/wsl.conf inside WSL2:" -ForegroundColor White
        Write-Host "      [boot]" -ForegroundColor DarkGray
        Write-Host "      systemd=true" -ForegroundColor DarkGray
        Write-Host "  Then run: wsl --shutdown" -ForegroundColor White
        Write-Host ""
        $proceed = Read-Host "  Create task anyway? [Y/N]"
        if ($proceed -notmatch '^[Yy]') {
            Write-Host "  Skipped auto-start setup." -ForegroundColor Gray
            $setupAutoStart = 'N'
        }
    }
}
if ($setupAutoStart -match '^[Yy]') {
    $taskName = "SpiralPool-WSL2-Autostart"

    # Remove existing task if present so we can recreate cleanly
    $existing = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Host ""
        Write-Host "  Task '$taskName' already exists." -ForegroundColor Yellow
        $replace = Read-Host "  Replace it? [Y/N]"
        if ($replace -match '^[Yy]') {
            Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
        } else {
            Write-Host "  Kept existing task." -ForegroundColor Gray
            $setupAutoStart = 'N'
        }
    }

    if ($setupAutoStart -match '^[Yy]') {
        # Start WSL2 and bring up all Spiral Pool systemd services.
        # The sleep gives systemd time to initialise before the service start commands run.
        $startServices = "sleep 15 && systemctl start spiralstratum spiralsentinel spiraldash spiralpool-health"
        $distroArg = if ($selectedDistro) { "-d `"$selectedDistro`" " } else { "" }
        $action    = New-ScheduledTaskAction -Execute "wsl.exe" -Argument "${distroArg}--user root --exec bash -c `"$startServices`""
        $trigger   = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
        $principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Highest
        $settings  = New-ScheduledTaskSettingsSet `
                         -StartWhenAvailable `
                         -ExecutionTimeLimit (New-TimeSpan -Hours 0) `
                         -RestartCount 3 `
                         -RestartInterval (New-TimeSpan -Minutes 2) `
                         -Hidden

        try {
            Register-ScheduledTask `
                -TaskName    $taskName `
                -Action      $action `
                -Trigger     $trigger `
                -Principal   $principal `
                -Settings    $settings `
                -Description "Starts Spiral Pool services inside WSL2 at Windows logon." | Out-Null

            Write-Host ""
            Write-Host "  Auto-start task created: $taskName" -ForegroundColor Green
            Write-Host "  Spiral Pool services will start automatically at next logon." -ForegroundColor Gray
            Write-Host ""
            Write-Host "  Notes:" -ForegroundColor White
            Write-Host "  - Task runs at logon, not at startup — you must be logged in." -ForegroundColor Gray
            Write-Host "  - Requires systemd enabled in WSL2 (/etc/wsl.conf: [boot] systemd=true)." -ForegroundColor Gray
            Write-Host "  - install.sh enables all services automatically (spiralstratum, spiralsentinel," -ForegroundColor Gray
            Write-Host "    spiraldash, spiralpool-health). If you re-installed, re-enable with:" -ForegroundColor Gray
            Write-Host "    sudo systemctl enable spiralstratum spiralsentinel spiraldash spiralpool-health" -ForegroundColor Gray
            Write-Host "  - To remove: Task Scheduler > Task Scheduler Library > $taskName" -ForegroundColor Gray
            Write-Host "  - This proxy window (portproxy rules) still needs to be run separately." -ForegroundColor Gray
        } catch {
            Write-Host ""
            Write-Host "  [!] Could not register auto-start task: $_" -ForegroundColor Yellow
            Write-Host "  Portproxy rules are active. Start services manually when needed." -ForegroundColor Gray
        }
    }
}
Write-Host ""

# ─── Auto-start portproxy rules on logon ────────────────────────────────────────────
Write-Host "  ─────────────────────────────────────────────────────────────" -ForegroundColor Cyan
Write-Host "  AUTO-START PORTPROXY RULES" -ForegroundColor White
Write-Host "  ─────────────────────────────────────────────────────────────" -ForegroundColor Cyan
Write-Host ""
Write-Host "  Portproxy rules are wiped on every Windows restart. A second Task" -ForegroundColor White
Write-Host "  Scheduler entry can re-apply them automatically at logon:" -ForegroundColor White
Write-Host "    - Waits up to 60 s for WSL2 to get its new IP" -ForegroundColor Gray
Write-Host "    - Re-applies portproxy rules for the current coin selection" -ForegroundColor Gray
Write-Host "    - Exits silently (rules persist until next reboot)" -ForegroundColor Gray
Write-Host ""
$setupProxyAutoStart = Read-Host "  Auto-start portproxy rules at logon? [Y/N]"
if ($setupProxyAutoStart -match '^[Yy]') {
    $proxyTaskName = "SpiralPool-WSL2-PortProxy"

    $existingProxy = Get-ScheduledTask -TaskName $proxyTaskName -ErrorAction SilentlyContinue
    if ($existingProxy) { Unregister-ScheduledTask -TaskName $proxyTaskName -Confirm:$false }

    # Store helper script in %APPDATA%\SpiralPool\
    $appDataDir = "$env:APPDATA\SpiralPool"
    if (-not (Test-Path $appDataDir)) { New-Item -ItemType Directory -Path $appDataDir | Out-Null }

    $portsLiteral = ($addedPorts | ForEach-Object { $_.ToString() }) -join ','
    $distroLiteral = if ($selectedDistro) { $selectedDistro } else { "" }
    $helperContent = @"
# Auto-generated by Spiral Pool WSL2 Port Proxy Manager.
# Re-applies portproxy rules at logon. Regenerate via start-wsl2-proxy.bat.

`$ports  = @($portsLiteral)
`$distro = "$distroLiteral"

# Wait up to 60 s for WSL2 to acquire an IP
`$wslIP = `$null
`$attempts = 0
while (-not `$wslIP -and `$attempts -lt 6) {
    Start-Sleep -Seconds 10
    `$attempts++
    try {
        `$wslArgs = if (`$distro) { @('-d', `$distro, '--exec', 'hostname', '-I') } else { @('--exec', 'hostname', '-I') }
        `$raw = (& wsl @`$wslArgs).Trim()
        `$wslIP = (`$raw.Split(' ') | Where-Object { `$_ -match '^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$' } | Select-Object -First 1)
    } catch {}
}
if (-not `$wslIP) { exit 1 }

foreach (`$port in `$ports) {
    `$r = netsh interface portproxy add v4tov4 listenport=`$port listenaddress=0.0.0.0 connectport=`$port connectaddress=`$wslIP 2>&1
    if (`$LASTEXITCODE -ne 0) { [System.Console]::Error.WriteLine("portproxy failed port `$port: `$r") }
}
"@
    $helperPath = "$appDataDir\spiralpool-proxy-autostart.ps1"
    try {
        Set-Content -Path $helperPath -Value $helperContent -Encoding UTF8

        $proxyAction    = New-ScheduledTaskAction -Execute "powershell.exe" `
                              -Argument "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$helperPath`""
        $proxyTrigger   = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
        $proxyPrincipal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Highest
        $proxySettings  = New-ScheduledTaskSettingsSet -StartWhenAvailable -ExecutionTimeLimit (New-TimeSpan -Minutes 5) -Hidden

        Register-ScheduledTask `
            -TaskName   $proxyTaskName `
            -Action     $proxyAction `
            -Trigger    $proxyTrigger `
            -Principal  $proxyPrincipal `
            -Settings   $proxySettings `
            -Description "Re-applies Spiral Pool portproxy rules for WSL2 at Windows logon." | Out-Null

        Write-Host ""
        Write-Host "  Portproxy auto-start task created: $proxyTaskName" -ForegroundColor Green
        Write-Host "  Helper script saved to: $helperPath" -ForegroundColor Gray
        Write-Host "  Portproxy rules will be applied automatically at next logon." -ForegroundColor Gray
        Write-Host "  Note: portproxy rules are wiped on every Windows reboot;" -ForegroundColor Gray
        Write-Host "    this task re-applies them at each logon automatically." -ForegroundColor Gray
        Write-Host "  Note: re-run this script if you change your coin selection." -ForegroundColor Gray
        Write-Host "  To remove: Task Scheduler > Task Scheduler Library > $proxyTaskName" -ForegroundColor Gray
        Write-Host "  To list all tasks: Get-ScheduledTask -TaskName SpiralPool*" -ForegroundColor Gray
    } catch {
        Write-Host ""
        Write-Host "  [!] Could not create portproxy auto-start task: $_" -ForegroundColor Yellow
        Write-Host "  Portproxy rules are active for this session." -ForegroundColor Gray
        Write-Host "  Re-run this script after each reboot to restore port forwarding." -ForegroundColor Gray
    }
}
Write-Host ""

# ─── Wait loop — cleanup portproxy rules on exit ──────────────────────────────
# try/finally fires on Ctrl+C so rules are always removed cleanly.
# Firewall rules are left in place (they are harmless allow-rules).
try {
    $tick = 0
    while ($true) {
        Start-Sleep -Seconds 10
        $tick++
        # Print a heartbeat every 60 seconds so the window looks alive
        if ($tick % 6 -eq 0) {
            $ts = Get-Date -Format 'HH:mm:ss'
            # Check for WSL2 IP drift (happens when WSL2 restarts mid-session)
            try {
                $driftArgs = if ($selectedDistro) { @('-d', $selectedDistro, '--exec', 'hostname', '-I') } else { @('--exec', 'hostname', '-I') }
                $currentRaw = (& wsl @driftArgs).Trim()
                $currentWSL = ($currentRaw.Split(' ') | Where-Object { $_ -match '^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$' } | Select-Object -First 1)
                if ($currentWSL -and $currentWSL -ne $wslIP) {
                    Write-Host ""
                    Write-Host "  [!] WSL2 IP changed: $wslIP -> $currentWSL  Updating portproxy rules..." -ForegroundColor Yellow
                    foreach ($port in $addedPorts) {
                        netsh interface portproxy delete v4tov4 listenport=$port listenaddress=0.0.0.0 | Out-Null
                    }
                    $wslIP = $currentWSL
                    $addedPorts = @()
                    foreach ($port in $selectedPorts) {
                        $r = netsh interface portproxy add v4tov4 listenport=$port listenaddress=0.0.0.0 connectport=$port connectaddress=$wslIP 2>&1
                        if ($LASTEXITCODE -eq 0) { $addedPorts += $port }
                        else { Write-Host "  [!] netsh failed for port $port: $r" -ForegroundColor Red }
                    }
                    Write-Host "  [+] Rules updated -> $wslIP  ($($addedPorts.Count) rule(s) active)" -ForegroundColor Green
                }
            } catch {
                Write-Host "  [!] Could not check WSL2 IP: $_" -ForegroundColor Yellow
            }
            Write-Host ("  [{0}]  Proxy running  ({1} rule(s) active)  WSL2: {2}" -f $ts, $addedPorts.Count, $wslIP) -ForegroundColor DarkGray
        }
    }
} finally {
    Write-Host ""
    Write-Host "  Stopping — removing portproxy rules..." -ForegroundColor Yellow
    foreach ($port in $addedPorts) {
        netsh interface portproxy delete v4tov4 listenport=$port listenaddress=0.0.0.0 | Out-Null
        Write-Host ("  -  Removed rule for port {0}" -f $port) -ForegroundColor Gray
    }
    Write-Host ""
    Write-Host "  Portproxy rules removed. Pool proxy stopped." -ForegroundColor Green
    Write-Host "  Note: Windows Firewall inbound rules (SpiralPool-Stratum-*) are kept." -ForegroundColor DarkGray
    Write-Host "  They are harmless but accumulate if you change coins." -ForegroundColor DarkGray
    Write-Host "  Remove stale ones via: Get-NetFirewallRule -DisplayName SpiralPool*" -ForegroundColor DarkGray
    Write-Host ""
}
