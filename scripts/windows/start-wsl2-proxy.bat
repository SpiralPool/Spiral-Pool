@echo off
REM SPDX-License-Identifier: BSD-3-Clause
REM SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
REM
REM start-wsl2-proxy.bat
REM Launches the WSL2 port proxy manager for Spiral Pool.
REM Handles WSL2 installation (if needed) and routes ASIC/miner
REM traffic from the Windows LAN IP into the WSL2 stratum server.
REM
REM Run as Administrator (script will prompt for elevation if needed).

setlocal

REM --- Self-elevate if not already running as Administrator ---
net session >nul 2>&1
if %errorLevel% neq 0 (
    echo.
    echo  Requesting Administrator privileges...
    echo.
    powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs -Wait"
    exit /b
)

REM --- Locate the PowerShell script relative to this .bat file ---
set "PS_SCRIPT=%~dp0scripts\windows\wsl2-stratum-proxy.ps1"

if not exist "%PS_SCRIPT%" (
    echo.
    echo  [ERROR] Could not find: scripts\windows\wsl2-stratum-proxy.ps1
    echo.
    echo  Make sure you are running this from the Spiral Pool repository root.
    echo.
    pause
    exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%PS_SCRIPT%"

echo.
pause
endlocal
