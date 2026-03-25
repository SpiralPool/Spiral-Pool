# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
#
# wsl2-shutdown-hook.ps1
# Gracefully stops all Spiral Pool and coin daemon services inside WSL2
# BEFORE Windows shuts down, restarts, or sleeps.
#
# Without this, Windows kills the WSL2 VM mid-operation, which can corrupt
# LevelDB chain data (blocks/chainstate) and require a full resync.
#
# Installs a Windows Task Scheduler entry triggered on:
#   - System shutdown/restart (Event ID 1074)
#   - Sleep/hibernate (custom event trigger)
#
# Usage:
#   .\wsl2-shutdown-hook.ps1              # Install the hook
#   .\wsl2-shutdown-hook.ps1 -Uninstall   # Remove the hook
#   .\wsl2-shutdown-hook.ps1 -Run         # Execute the shutdown sequence (called by Task Scheduler)

param(
    [switch]$Uninstall,
    [switch]$Run
)

$ErrorActionPreference = 'Stop'
$TASK_NAME = "SpiralPool-WSL2-GracefulShutdown"
$HELPER_DIR = "$env:APPDATA\SpiralPool"
$HELPER_SCRIPT = "$HELPER_DIR\spiralpool-wsl2-shutdown.ps1"

# ─── Run mode: called by Task Scheduler at shutdown/sleep ──────────────────
if ($Run) {
    $logFile = "$HELPER_DIR\shutdown-hook.log"
    $ts = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'

    # Find the WSL2 distro that has Spiral Pool installed
    $distro = $null
    try {
        $origEncoding = [Console]::OutputEncoding
        [Console]::OutputEncoding = [System.Text.Encoding]::Unicode
        $wslList = & wsl -l -q 2>&1
        [Console]::OutputEncoding = $origEncoding
        $distros = @($wslList | Where-Object { $_ -match '\S' -and $_ -notmatch 'Windows Subsystem' } |
                     ForEach-Object { $_.Trim().TrimEnd('*') })
        if ($distros.Count -ge 1) { $distro = $distros[0] }
    } catch {}

    if (-not $distro) {
        Add-Content -Path $logFile -Value "$ts  No WSL2 distro found. Skipping."
        exit 0
    }

    # Check if WSL2 is actually running
    try {
        $state = & wsl -l --running 2>&1
        if ($state -notmatch $distro) {
            Add-Content -Path $logFile -Value "$ts  WSL2 distro '$distro' not running. Skipping."
            exit 0
        }
    } catch {
        Add-Content -Path $logFile -Value "$ts  Could not check WSL2 state: $_"
        exit 0
    }

    Add-Content -Path $logFile -Value "$ts  Graceful shutdown starting for distro: $distro"

    # Stop Spiral Pool services first, then coin daemons
    # Order matters: sentinel/dash first, then stratum (flushes shares), then daemons
    $stopCmd = @(
        "systemctl stop spiralsentinel 2>/dev/null"
        "systemctl stop spiraldash 2>/dev/null"
        "systemctl stop spiralpool-health 2>/dev/null"
        "systemctl stop spiralpool-ha-watcher 2>/dev/null"
        "systemctl stop spiralstratum 2>/dev/null"
        "for svc in `$(systemctl list-units --type=service --state=running --no-legend | grep -oP 'bitcoind-\S+|litecoind\S+|dogecoind\S+|digibyted\S+|namecoind\S+|syscoind\S+|pepcoind\S+|catcoind\S+|fractal-bitcoind\S+|qbitxd\S+|myriadcoind\S+' | head -20); do systemctl stop `$svc 2>/dev/null; done"
        "sync"
    ) -join ' && '

    try {
        $wslArgs = @('-d', $distro, '--user', 'root', '--exec', 'bash', '-c', $stopCmd)
        $result = & wsl @wslArgs 2>&1
        $exitCode = $LASTEXITCODE
        Add-Content -Path $logFile -Value "$ts  Stop command exit code: $exitCode"
        if ($result) { Add-Content -Path $logFile -Value "$ts  Output: $result" }
    } catch {
        Add-Content -Path $logFile -Value "$ts  Error running stop commands: $_"
    }

    Add-Content -Path $logFile -Value "$ts  Graceful shutdown complete."
    exit 0
}

# ─── Uninstall mode ────────────────────────────────────────────────────────
if ($Uninstall) {
    Write-Host ""
    $existing = Get-ScheduledTask -TaskName $TASK_NAME -ErrorAction SilentlyContinue
    if ($existing) {
        Unregister-ScheduledTask -TaskName $TASK_NAME -Confirm:$false
        Write-Host "  Removed task: $TASK_NAME" -ForegroundColor Green
    } else {
        Write-Host "  Task '$TASK_NAME' not found. Nothing to remove." -ForegroundColor Gray
    }
    if (Test-Path $HELPER_SCRIPT) {
        Remove-Item -Path $HELPER_SCRIPT -Force
        Write-Host "  Removed helper: $HELPER_SCRIPT" -ForegroundColor Green
    }
    Write-Host ""
    exit 0
}

# ─── Install mode (default) ───────────────────────────────────────────────

# Elevation check
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host ""
    Write-Host "  [!] This script must run as Administrator." -ForegroundColor Red
    Write-Host "      Right-click PowerShell > Run as Administrator, then try again." -ForegroundColor Yellow
    Write-Host ""
    exit 1
}

Write-Host ""
Write-Host "  ╔══════════════════════════════════════════════════════════════╗" -ForegroundColor Cyan
Write-Host "  ║  SPIRAL POOL  -  WSL2 Graceful Shutdown Hook               ║" -ForegroundColor Cyan
Write-Host "  ╚══════════════════════════════════════════════════════════════╝" -ForegroundColor Cyan
Write-Host ""
Write-Host "  This installs a Windows Task Scheduler entry that gracefully" -ForegroundColor White
Write-Host "  stops all Spiral Pool services and coin daemons inside WSL2" -ForegroundColor White
Write-Host "  BEFORE Windows shuts down, restarts, or sleeps." -ForegroundColor White
Write-Host ""
Write-Host "  Without this hook, Windows kills WSL2 mid-operation, which" -ForegroundColor Yellow
Write-Host "  can corrupt LevelDB chain data and require a full resync." -ForegroundColor Yellow
Write-Host ""

# Check for existing task
$existing = Get-ScheduledTask -TaskName $TASK_NAME -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "  Task '$TASK_NAME' already exists." -ForegroundColor Yellow
    $replace = Read-Host "  Replace it? [Y/N]"
    if ($replace -notmatch '^[Yy]') {
        Write-Host "  Kept existing task. No changes made." -ForegroundColor Gray
        Write-Host ""
        exit 0
    }
    Unregister-ScheduledTask -TaskName $TASK_NAME -Confirm:$false
    Write-Host "  Removed existing task." -ForegroundColor Gray
}

# Create helper directory
if (-not (Test-Path $HELPER_DIR)) { New-Item -ItemType Directory -Path $HELPER_DIR | Out-Null }

# Write the helper script that Task Scheduler will invoke
# (It just calls this same script with -Run)
$thisScript = $MyInvocation.MyCommand.Path
if (-not $thisScript) {
    # Fallback: write a standalone helper
    $thisScript = $HELPER_SCRIPT
}

# Copy this script to AppData so it persists independent of the repo checkout
Copy-Item -Path $PSCommandPath -Destination $HELPER_SCRIPT -Force
Write-Host "  Helper script saved to: $HELPER_SCRIPT" -ForegroundColor Gray

# Create the scheduled task
# Trigger: System Event 1074 (shutdown/restart initiated by user or process)
# We use an Event trigger on the System log for shutdown events.
# For sleep/hibernate we use a second trigger on Event ID 506 (power transition).

$shutdownTriggerXml = @"
<QueryList>
  <Query Id="0" Path="System">
    <Select Path="System">*[System[Provider[@Name='User32'] and (EventID=1074)]]</Select>
  </Query>
</QueryList>
"@

$action = New-ScheduledTaskAction `
    -Execute "powershell.exe" `
    -Argument "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$HELPER_SCRIPT`" -Run"

# Use a subscription trigger for shutdown events
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME  # placeholder, replaced by XML

$principal = New-ScheduledTaskPrincipal `
    -UserId "SYSTEM" `
    -LogonType ServiceAccount `
    -RunLevel Highest

$settings = New-ScheduledTaskSettingsSet `
    -StartWhenAvailable `
    -ExecutionTimeLimit (New-TimeSpan -Minutes 2) `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -Priority 1

# Register with a basic trigger first, then modify the XML to use event triggers
Register-ScheduledTask `
    -TaskName    $TASK_NAME `
    -Action      $action `
    -Trigger     $trigger `
    -Principal   $principal `
    -Settings    $settings `
    -Description "Gracefully stops Spiral Pool services in WSL2 before Windows shuts down, restarts, or sleeps. Prevents LevelDB corruption." | Out-Null

# Now replace the trigger with proper event-based triggers via XML modification
$task = Get-ScheduledTask -TaskName $TASK_NAME
$taskXml = Export-ScheduledTask -TaskName $TASK_NAME

# Replace the trigger section with event-based triggers
$newTriggersXml = @"
  <Triggers>
    <EventTrigger>
      <Enabled>true</Enabled>
      <Subscription>&lt;QueryList&gt;&lt;Query Id="0" Path="System"&gt;&lt;Select Path="System"&gt;*[System[Provider[@Name='User32'] and (EventID=1074)]]&lt;/Select&gt;&lt;/Query&gt;&lt;/QueryList&gt;</Subscription>
    </EventTrigger>
    <EventTrigger>
      <Enabled>true</Enabled>
      <Subscription>&lt;QueryList&gt;&lt;Query Id="0" Path="System"&gt;&lt;Select Path="System"&gt;*[System[Provider[@Name='Microsoft-Windows-Kernel-Power'] and (EventID=42)]]&lt;/Select&gt;&lt;/Query&gt;&lt;/QueryList&gt;</Subscription>
    </EventTrigger>
  </Triggers>
"@

# Replace triggers in the exported XML
$taskXml = $taskXml -replace '(?s)<Triggers>.*?</Triggers>', $newTriggersXml

# Unregister and re-register with the corrected XML
Unregister-ScheduledTask -TaskName $TASK_NAME -Confirm:$false
Register-ScheduledTask -TaskName $TASK_NAME -Xml $taskXml | Out-Null

Write-Host ""
Write-Host "  ╔══════════════════════════════════════════════════════════════╗" -ForegroundColor Green
Write-Host "  ║  SHUTDOWN HOOK INSTALLED                                    ║" -ForegroundColor Green
Write-Host "  ╠══════════════════════════════════════════════════════════════╣" -ForegroundColor Green
Write-Host "  ║  Task: $($TASK_NAME.PadRight(49))║" -ForegroundColor White
Write-Host "  ║                                                              ║" -ForegroundColor White
Write-Host "  ║  Triggers:                                                   ║" -ForegroundColor White
Write-Host "  ║    - Windows shutdown / restart  (Event 1074)               ║" -ForegroundColor Gray
Write-Host "  ║    - Windows sleep / hibernate   (Event 42)                 ║" -ForegroundColor Gray
Write-Host "  ║                                                              ║" -ForegroundColor White
Write-Host "  ║  Stop order:                                                 ║" -ForegroundColor White
Write-Host "  ║    1. spiralsentinel, spiraldash, health, ha-watcher        ║" -ForegroundColor Gray
Write-Host "  ║    2. spiralstratum (flushes shares to DB)                  ║" -ForegroundColor Gray
Write-Host "  ║    3. All coin daemons (clean LevelDB shutdown)             ║" -ForegroundColor Gray
Write-Host "  ║    4. sync (flush filesystem buffers)                       ║" -ForegroundColor Gray
Write-Host "  ║                                                              ║" -ForegroundColor White
Write-Host "  ║  Log: %APPDATA%\SpiralPool\shutdown-hook.log                ║" -ForegroundColor Gray
Write-Host "  ║                                                              ║" -ForegroundColor White
Write-Host "  ║  To remove:  .\wsl2-shutdown-hook.ps1 -Uninstall            ║" -ForegroundColor Gray
Write-Host "  ╚══════════════════════════════════════════════════════════════╝" -ForegroundColor Green
Write-Host ""
