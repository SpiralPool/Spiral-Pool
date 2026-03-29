@echo off
REM SPDX-License-Identifier: BSD-3-Clause
REM SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

REM ═══════════════════════════════════════════════════════════════════════════════
REM spiralpool-add-coin - Add new cryptocurrency support to Spiral Pool (Windows)
REM ═══════════════════════════════════════════════════════════════════════════════
REM
REM This command automates adding new SHA256d or Scrypt coin support.
REM It fetches chain parameters from GitHub, queries CoinGecko for metadata,
REM and generates both Go implementation and manifest entries.
REM
REM PREREQUISITES:
REM   - Python 3.x (auto-installed via winget if missing)
REM   - PyYAML package (optional but recommended)
REM
REM POST-GENERATION:
REM   - Configures Windows Firewall rules for stratum ports
REM   - Requires Docker image rebuild to activate
REM
REM USAGE:
REM   spiralpool-add-coin -s SYMBOL -g GITHUB_URL [OPTIONS]
REM
REM EXAMPLES:
REM   spiralpool-add-coin -s DOGE -g https://github.com/dogecoin/dogecoin
REM   spiralpool-add-coin -s LTC -g https://github.com/litecoin-project/litecoin --algorithm scrypt
REM   spiralpool-add-coin -s NEWCOIN --interactive
REM
REM ═══════════════════════════════════════════════════════════════════════════════

setlocal EnableDelayedExpansion

REM Find the script directory
set "SCRIPT_DIR=%~dp0"
set "PYTHON_SCRIPT=%SCRIPT_DIR%add-coin.py"
set "FIREWALL_SCRIPT=%SCRIPT_DIR%configure-coin-firewall.ps1"

REM Parse coin symbol from arguments for firewall configuration
set "COIN_SYMBOL="
set "IS_DRY_RUN="
set "SKIP_FIREWALL="
set "PREV_ARG="
for %%a in (%*) do (
    if "!PREV_ARG!"=="-s" set "COIN_SYMBOL=%%a"
    if "!PREV_ARG!"=="--symbol" set "COIN_SYMBOL=%%a"
    if "%%a"=="--dry-run" set "IS_DRY_RUN=1"
    if "%%a"=="--skip-firewall" set "SKIP_FIREWALL=1"
    set "PREV_ARG=%%a"
)

REM Check if Python script exists
if not exist "%PYTHON_SCRIPT%" (
    echo Error: Cannot find add-coin.py script at %PYTHON_SCRIPT%
    echo Please run this command from the Spiral Pool directory.
    exit /b 1
)

REM ═══════════════════════════════════════════════════════════════════════════════
REM PYTHON PREREQUISITE CHECK
REM ═══════════════════════════════════════════════════════════════════════════════

REM Check for Python
where python >nul 2>nul
if %ERRORLEVEL% neq 0 (
    echo.
    echo ═══════════════════════════════════════════════════════════════════════════════
    echo   Python is required but not installed.
    echo ═══════════════════════════════════════════════════════════════════════════════
    echo.

    REM Check for winget
    where winget >nul 2>nul
    if !ERRORLEVEL! neq 0 (
        echo   winget is not available. Please install Python manually:
        echo.
        echo   Option 1: Install winget from Microsoft Store, then run this script again
        echo   Option 2: Download Python directly from:
        echo            https://www.python.org/downloads/
        echo   Option 3: Download Python Manager:
        echo            https://www.python.org/ftp/python/pymanager/python-manager-25.2.msix
        echo.
        echo ═══════════════════════════════════════════════════════════════════════════════
        exit /b 1
    )

    echo   Attempting to install Python via winget...
    echo.

    REM Install Python from Microsoft Store via winget
    REM 9NRWMJP3717K is the Microsoft Store ID for Python 3.12
    winget install 9NRWMJP3717K --accept-package-agreements --accept-source-agreements

    if !ERRORLEVEL! neq 0 (
        echo.
        echo   winget installation failed. Trying alternative package...
        winget install Python.Python.3.12 --accept-package-agreements --accept-source-agreements

        if !ERRORLEVEL! neq 0 (
            echo.
            echo   ERROR: Failed to install Python automatically.
            echo.
            echo   Please install Python manually from:
            echo     https://www.python.org/downloads/
            echo.
            echo   Or download the Python Manager:
            echo     https://www.python.org/ftp/python/pymanager/python-manager-25.2.msix
            echo.
            echo ═══════════════════════════════════════════════════════════════════════════════
            exit /b 1
        )
    )

    echo.
    echo   Python installed successfully!
    echo.
    echo   IMPORTANT: You may need to restart your terminal or log out/in
    echo              for Python to be available in your PATH.
    echo.
    echo   After restarting, run this command again.
    echo ═══════════════════════════════════════════════════════════════════════════════
    exit /b 0
)

REM Verify Python version (must be 3.x)
for /f "tokens=2 delims= " %%v in ('python --version 2^>^&1') do set PYTHON_VERSION=%%v
echo Python found: version %PYTHON_VERSION%

REM Check if it's Python 3.x (starts with "3.")
echo %PYTHON_VERSION% | findstr /b "3." >nul
if %ERRORLEVEL% neq 0 (
    echo.
    echo WARNING: Python 3.x is required, but found version %PYTHON_VERSION%
    echo.
    echo Installing Python 3 via winget...

    where winget >nul 2>nul
    if !ERRORLEVEL! equ 0 (
        winget install Python.Python.3.12 --accept-package-agreements --accept-source-agreements
        echo.
        echo Please restart your terminal and run this command again.
        exit /b 1
    ) else (
        echo Please install Python 3 from: https://www.python.org/downloads/
        exit /b 1
    )
)

REM ═══════════════════════════════════════════════════════════════════════════════
REM OPTIONAL: Check for PyYAML
REM ═══════════════════════════════════════════════════════════════════════════════

python -c "import yaml" >nul 2>nul
if %ERRORLEVEL% neq 0 (
    echo.
    echo Note: PyYAML not installed. Installing for better manifest handling...
    pip install pyyaml >nul 2>nul
    if !ERRORLEVEL! equ 0 (
        echo PyYAML installed successfully.
    ) else (
        echo Warning: Could not install PyYAML. Some features may be limited.
    )
)

REM ═══════════════════════════════════════════════════════════════════════════════
REM MAIN SCRIPT EXECUTION
REM ═══════════════════════════════════════════════════════════════════════════════

REM Create temp file for capturing Python output
set "TEMP_OUTPUT=%TEMP%\spiralpool-add-coin-%RANDOM%%RANDOM%%RANDOM%.txt"

REM Show help if no arguments
if "%~1"=="" (
    echo ═══════════════════════════════════════════════════════════════════════════════
    echo   spiralpool-add-coin - Add new cryptocurrency support to Spiral Pool
    echo ═══════════════════════════════════════════════════════════════════════════════
    echo.
    echo USAGE:
    echo   spiralpool-add-coin -s SYMBOL -g GITHUB_URL [OPTIONS]
    echo.
    echo EXAMPLES:
    echo   spiralpool-add-coin -s DOGE -g https://github.com/dogecoin/dogecoin
    echo   spiralpool-add-coin -s LTC -g https://github.com/litecoin-project/litecoin -a scrypt
    echo   spiralpool-add-coin -s NEWCOIN --interactive
    echo   spiralpool-add-coin -s NEWCOIN -g https://github.com/newcoin/newcoin --dry-run
    echo.
    echo OPTIONS:
    echo   -s, --symbol      Coin ticker symbol ^(required^)
    echo   -g, --github      GitHub repository URL ^(enables full automation^)
    echo   -a, --algorithm   Override algorithm detection ^(sha256d or scrypt^)
    echo   -n, --name        Override coin name
    echo   -i, --interactive Enter interactive mode for manual data entry
    echo   -j, --from-json   Load parameters from previously saved JSON file
    echo   --dry-run         Preview output without writing files
    echo   --force           Overwrite existing files without prompting
    echo   --skip-firewall   Skip Windows Firewall configuration
    echo   -h, --help        Show this help message
    echo.
    echo PREREQUISITES:
    echo   - Python 3.x ^(auto-installed if missing^)
    echo   - PyYAML ^(auto-installed if missing^)
    echo.
    echo POST-GENERATION:
    echo   - Windows Firewall rules created for stratum ports
    echo   - Docker images must be rebuilt to activate coin support
    echo.
    echo For more details, see: docs\development\COIN_ONBOARDING_SPEC.md
    echo ═══════════════════════════════════════════════════════════════════════════════
    exit /b 0
)

REM Pass all arguments to the Python script and capture output
python "%PYTHON_SCRIPT%" %* > "%TEMP_OUTPUT%" 2>&1
set PYTHON_EXIT_CODE=%ERRORLEVEL%

REM Display the Python output to the user
type "%TEMP_OUTPUT%"

REM Parse stratum ports from Python output (format: __STRATUM_PORTS__:V1:V2:TLS)
set "STRATUM_V1_PORT="
set "STRATUM_V2_PORT="
set "STRATUM_TLS_PORT="
for /f "tokens=2,3,4 delims=:" %%a in ('findstr /C:"__STRATUM_PORTS__" "%TEMP_OUTPUT%" 2^>nul') do (
    set "STRATUM_V1_PORT=%%a"
    set "STRATUM_V2_PORT=%%b"
    set "STRATUM_TLS_PORT=%%c"
)

REM Clean up temp file
if exist "%TEMP_OUTPUT%" del "%TEMP_OUTPUT%"

REM Check if script succeeded
if %PYTHON_EXIT_CODE% equ 0 (
    REM Skip firewall for dry-run
    if defined IS_DRY_RUN (
        echo.
        echo [DRY RUN] Skipping firewall configuration.
        goto :show_docker_reminder
    )

    REM Skip firewall if explicitly requested
    if defined SKIP_FIREWALL (
        echo.
        echo [--skip-firewall] Skipping firewall configuration as requested.
        goto :show_docker_reminder
    )

    REM ═══════════════════════════════════════════════════════════════════════════════
    REM FIREWALL CONFIGURATION
    REM ═══════════════════════════════════════════════════════════════════════════════

    if defined COIN_SYMBOL (
        if exist "%FIREWALL_SCRIPT%" (
            echo.
            echo ═══════════════════════════════════════════════════════════════════════════════
            echo   Configuring Windows Firewall for %COIN_SYMBOL%...
            echo ═══════════════════════════════════════════════════════════════════════════════
            echo.

            REM Build firewall script arguments
            set "FW_ARGS=-Symbol %COIN_SYMBOL%"
            if defined STRATUM_V1_PORT (
                set "FW_ARGS=!FW_ARGS! -StratumV1Port !STRATUM_V1_PORT!"
                echo   Stratum ports detected from coin generation:
                echo     V1: !STRATUM_V1_PORT!, V2: !STRATUM_V2_PORT!, TLS: !STRATUM_TLS_PORT!
                echo.
            )

            REM Check if running as admin
            net session >nul 2>&1
            if !ERRORLEVEL! equ 0 (
                REM Running as admin - configure firewall directly
                powershell -ExecutionPolicy Bypass -File "%FIREWALL_SCRIPT%" !FW_ARGS! -Force
                if !ERRORLEVEL! neq 0 (
                    echo.
                    echo   [!] Firewall configuration failed. You may need to configure manually.
                    echo.
                )
            ) else (
                REM Not running as admin - offer to elevate
                echo   Firewall configuration requires Administrator privileges.
                echo.
                echo   Options:
                echo     1. Run this command as Administrator to auto-configure firewall
                echo     2. Manually run the following command as Administrator:
                echo.
                echo        powershell -ExecutionPolicy Bypass -File "%FIREWALL_SCRIPT%" !FW_ARGS!
                echo.
                echo     3. Or manually create firewall rules in Windows Defender Firewall
                echo.

                set /p "ELEVATE=  Attempt to configure firewall now? (requires UAC prompt) [y/N]: "
                if /i "!ELEVATE!"=="y" (
                    echo.
                    echo   Requesting Administrator privileges...
                    powershell -Command "Start-Process powershell -ArgumentList '-ExecutionPolicy Bypass -File \"%FIREWALL_SCRIPT%\" !FW_ARGS! -Force' -Verb RunAs -Wait"
                    if !ERRORLEVEL! equ 0 (
                        echo   Firewall rules configured successfully.
                    ) else (
                        echo   Firewall configuration was cancelled or failed.
                    )
                ) else (
                    echo.
                    echo   Skipping firewall configuration.
                    echo   Remember to configure firewall rules before miners can connect.
                )
            )
        ) else (
            echo.
            echo   [!] Firewall configuration script not found at:
            echo       %FIREWALL_SCRIPT%
            echo.
            echo   You may need to manually configure Windows Firewall rules for stratum ports.
        )
    )

    :show_docker_reminder
    echo.
    echo ═══════════════════════════════════════════════════════════════════════════════
    echo   NEXT STEPS - Rebuild Docker images for changes to take effect:
    echo ═══════════════════════════════════════════════════════════════════════════════
    echo.
    echo     cd docker
    echo     docker compose -f docker-compose.yml build --no-cache
    echo     docker compose -f docker-compose.yml up -d
    echo.
    echo ═══════════════════════════════════════════════════════════════════════════════
)

exit /b %PYTHON_EXIT_CODE%
