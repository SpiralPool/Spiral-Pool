# Spiral Pool Windows Installation Guide

Two installation paths exist for running Spiral Pool on Windows 11. Both are **experimental** and intended for development, testing, and pool evaluation. For production mining, use Ubuntu 24.04 LTS with the native `install.sh` installer.

---

## Which Path Should I Use?

| | **Path A: Docker Desktop** | **Path B: WSL2 Native** |
|---|---|---|
| **What it does** | Runs everything in Docker containers on Windows | Installs the full Linux stack inside a WSL2 Ubuntu VM |
| **Installer** | `install-windows.ps1` (PowerShell) | `install.sh` (runs inside WSL2 Ubuntu) |
| **Windows editions** | Home, Pro, Enterprise, Education | Home, Pro, Enterprise, Education |
| **Coins** | Single coin only (14 choices) | All 14 coins, multi-coin, merge mining |
| **Stratum** | V1 + TLS | V1 + V2 + TLS |
| **Merge mining** | Not supported | Supported (BTC+NMC+SYS+XMY+FBTC, LTC+DOGE+PEP) |
| **High availability** | Not supported | Not supported (requires bare metal Linux) |
| **Blockchain sync speed** | Slow (Docker + WSL2 I/O overhead) | Slow (WSL2 I/O overhead) |
| **Miner connectivity** | Automatic (Docker publishes ports) | Requires [port forwarding script](../../scripts/windows/start-wsl2-proxy.bat) |
| **Setup difficulty** | Easy (fully automated) | Moderate (Linux command line inside WSL2) |
| **RAM requirement** | 4 GB minimum, 8 GB recommended | 10 GB minimum, 16 GB recommended |
| **Best for** | Quick evaluation, single-coin testing | Multi-coin testing, closer to production behavior |

### Decision Tree

1. **Just want to try it out?** &rarr; **Path A (Docker Desktop)**. One command, fully automated.
2. **Need multi-coin or merge mining?** &rarr; **Path B (WSL2 Native)**. Docker is single-coin only.
3. **Need V2 Stratum?** &rarr; **Path B (WSL2 Native)**.
4. **Planning to go to production?** &rarr; Skip Windows entirely. Install Ubuntu 24.04 LTS on bare metal or a dedicated VM.

---

## Path A: Docker Desktop (install-windows.ps1)

The Windows installer automates the full Docker Desktop deployment: virtualization backend (WSL2 or Hyper-V), Docker Desktop download/install, coin selection, wallet configuration, firewall rules, and auto-start.

### Prerequisites

- Windows 11 (build 22000+)
- Administrator privileges
- 8 GB RAM recommended (4 GB minimum)
- Storage varies by coin (5 GB to 600 GB)
- Internet connection for Docker Desktop download and blockchain sync

### Interactive Installation

```powershell
# Open PowerShell as Administrator
# Navigate to the Spiral Pool directory
cd "C:\path\to\Spiral-Pool"

# Run the installer
.\install-windows.ps1
```

The installer will:
1. Accept terms of use
2. Detect and install WSL2 or enable Hyper-V (reboot may be required)
3. Check RAM requirements
4. Download and install Docker Desktop (if not installed)
5. Present a coin selection menu (14 coins)
6. Prompt for wallet address, storage path, and coinbase text
7. Configure Windows Firewall rules
8. Generate `.env` and start Docker containers
9. Set up auto-start and health monitoring scheduled tasks

### Automated Installation

```powershell
.\install-windows.ps1 -Unattended -PoolAddress "DYourAddressHere" -Coin DGB -AcceptTerms

# With custom data drive
.\install-windows.ps1 -Unattended -PoolAddress "ltc1..." -Coin LTC -DataDrive "D:" -AcceptTerms
```

### Available Coins

| # | Symbol | Coin | Algorithm | Storage | Stratum Port |
|---|--------|------|-----------|---------|-------------|
| 1 | DGB | DigiByte | SHA256d | ~60 GB | 3333 |
| 2 | BTC | Bitcoin | SHA256d | ~600 GB | 4333 |
| 3 | BCH | Bitcoin Cash | SHA256d | ~250 GB | 5333 |
| 4 | BC2 | Bitcoin II | SHA256d | ~10 GB | 6333 |
| 5 | NMC | Namecoin | SHA256d | ~15 GB | 14335 |
| 6 | XMY | Myriadcoin | SHA256d | ~8 GB | 17335 |
| 7 | FBTC | Fractal Bitcoin | SHA256d | ~10 GB | 18335 |
| 8 | QBX | Q-BitX | SHA256d | ~5 GB | 20335 |
| 9 | LTC | Litecoin | Scrypt | ~150 GB | 7333 |
| 10 | DOGE | Dogecoin | Scrypt | ~80 GB | 8335 |
| 11 | DGB-SCRYPT | DigiByte (Scrypt) | Scrypt | ~60 GB | 3336 |
| 12 | PEP | PepeCoin | Scrypt | ~5 GB | 10335 |
| 13 | CAT | Catcoin | Scrypt | ~5 GB | 12335 |

> Syscoin (SYS) appears in the menu but cannot be mined standalone. It requires merge mining with BTC, which is only available via native Linux installation.

### Post-Installation

```powershell
# Navigate to the Docker directory
cd C:\SpiralPool\docker

# View logs
docker compose -f docker-compose.yml --profile dgb logs -f

# Check container status
docker compose -f docker-compose.yml --profile dgb ps

# Restart
docker compose -f docker-compose.yml --profile dgb restart

# Stop
docker compose -f docker-compose.yml --profile dgb down

# Start
docker compose -f docker-compose.yml --profile dgb up -d
```

Replace `dgb` with your coin's profile name (btc, bch, ltc, doge, etc.).

### What Gets Installed

| Component | Location / Details |
|-----------|-------------------|
| Docker Desktop | Standard install path |
| WSL2 or Hyper-V | Windows feature (enabled if needed) |
| Spiral Pool files | `C:\SpiralPool\` (or custom `DataDrive`) |
| Docker containers | Stratum, dashboard, database, blockchain node |
| Firewall rules | "Spiral Pool - *" (Private/Domain networks only) |
| Scheduled tasks | SpiralPoolStartup, SpiralPoolHealthCheck |
| Install log | `%TEMP%\spiralpool-install.log` |

### Limitations

- **Single coin only** &mdash; no multi-coin simultaneous mining
- **No merge mining** &mdash; BTC+NMC, LTC+DOGE pairs require native Linux
- **No V2 Stratum** &mdash; V1 + TLS only
- **No High Availability** &mdash; no VIP failover
- **Blockchain sync is slow** &mdash; Docker + WSL2 adds significant I/O overhead (2-4x slower than native)
- **Windows updates can interrupt mining** &mdash; Windows may restart without warning

---

## Path B: WSL2 Native (install.sh inside WSL2)

This path runs `install.sh` directly inside a WSL2 Ubuntu distribution. It provides the full Linux feature set (multi-coin, merge mining, V2 Stratum) but requires port forwarding for miners to connect through Windows and a shutdown hook to prevent blockchain corruption.

### Prerequisites

- Windows 11 (build 22000+)
- WSL2 with Ubuntu 24.04 LTS installed
- 16 GB RAM recommended (10 GB minimum)
- Storage varies by coin configuration

### Step 1: Install WSL2 and Ubuntu

```powershell
# Open PowerShell as Administrator
wsl --install -d Ubuntu-24.04
# Follow the prompts to create a Linux username and password
# Restart if prompted
```

### Step 2: Run the Installer Inside WSL2

```bash
# Open the Ubuntu terminal (Start menu > Ubuntu 24.04)
sudo apt update && sudo apt upgrade -y
sudo apt install -y git

git clone --depth 1 https://github.com/SpiralPool/Spiral-Pool.git
cd Spiral-Pool && sudo ./install.sh
```

The installer is the same one used on native Linux. It supports the full interactive menu with back navigation (`b`), checkpoint resume, multi-coin selection, merge mining configuration, and all 14 coins.

### Step 3: Install the Shutdown Hook

Windows can terminate WSL2 abruptly during shutdown, sleep, or hibernation. This corrupts LevelDB chain data. Install the shutdown hook to gracefully stop services first:

```powershell
# From PowerShell (not WSL2)
powershell -ExecutionPolicy Bypass -File "scripts\windows\wsl2-shutdown-hook.ps1" -Install
```

This creates a scheduled task that runs before Windows shutdown/sleep to gracefully stop Spiral Pool services inside WSL2.

### Step 4: Configure Port Forwarding

Miners on your LAN need to reach the stratum ports inside WSL2. Run the port forwarding script:

```batch
scripts\windows\start-wsl2-proxy.bat
```

This sets up `netsh portproxy` rules so that connections to your Windows IP are forwarded to the WSL2 VM. The WSL2 VM gets a new IP address on each restart, so this script should be run after each Windows reboot (or set up as a scheduled task).

### WSL2-Specific Considerations

- **Clock drift**: After Windows sleep/hibernate, the WSL2 clock can drift. The installer detects this and attempts `hwclock -s` to re-sync. If drift exceeds 60 seconds, restart WSL2: `wsl --shutdown` then reopen the terminal.
- **Read-only sysctl**: WSL2 shares the Windows kernel, so `kernel.dmesg_restrict`, `kernel.kptr_restrict`, `kernel.randomize_va_space`, and `fs.suid_dumpable` cannot be set. The installer handles this automatically.
- **IPv6 sysctl**: IPv6 disablement via sysctl is skipped on WSL2 (shared kernel limitation).
- **No `systemd-tmpfiles`**: On WSL2 distributions without full systemd, the installer falls back to `mkdir -p`.
- **Reboot**: `systemctl reboot` inside WSL2 shuts down the entire VM. To restart, run `wsl -d Ubuntu-24.04` from PowerShell.
- **Filesystem performance**: Store blockchain data on the Linux filesystem (`/spiralpool/data/`), not on Windows mounts (`/mnt/c/`). Cross-filesystem access through 9P is extremely slow.

---

## Connecting Miners

Both paths expose stratum ports on your Windows machine's IP address.

```
URL:      stratum+tcp://YOUR_WINDOWS_IP:PORT
Worker:   YOUR_WALLET_ADDRESS.worker_name
Password: x
```

Find your Windows IP:
```powershell
ipconfig | findstr "IPv4"
```

For TLS connections, use `stratum+ssl://` with the TLS port (typically stratum port + 2).

---

## Troubleshooting

### Docker Desktop won't start
- Ensure virtualization is enabled in BIOS/UEFI (VT-x / AMD-V)
- Check that WSL2 or Hyper-V is enabled: `wsl --status` or `Get-WindowsOptionalFeature -FeatureName Microsoft-Hyper-V -Online`
- Restart Windows after enabling virtualization features

### Miners can't connect (Path A: Docker)
- Check Windows Firewall: `Get-NetFirewallRule -DisplayName "Spiral Pool*"`
- Verify containers are running: `docker compose ps`
- Ensure your router isn't blocking the stratum port

### Miners can't connect (Path B: WSL2 Native)
- Re-run the port forwarding script after each Windows reboot
- Check that the WSL2 VM is running: `wsl -l --running`
- Verify the port proxy rules: `netsh interface portproxy show v4tov4`

### Blockchain sync is extremely slow
- This is expected on Windows. WSL2 adds I/O overhead. BTC (600 GB) may take weeks.
- Consider pruned mode if available for your coin
- For faster sync, install on native Linux

### WSL2 clock drift after sleep
```bash
# Inside WSL2
sudo hwclock -s
```

### "Docker not recognized" in scheduled tasks
- The health check and auto-start tasks run as your user account. If Docker Desktop is not on your user's PATH, the tasks will fail silently.
- Open Docker Desktop settings > General > "Start Docker Desktop when you sign in"

---

## Moving to Production

When you're ready to move from Windows testing to production:

1. Set up a dedicated Ubuntu 24.04 LTS server (bare metal or self-hosted VM)
2. Run `sudo ./install.sh` on the Linux server
3. Point your miners to the new server's IP
4. The Linux installer supports everything: multi-coin, merge mining, V2 Stratum, HA, and full `spiralctl` management

See [OPERATIONS.md](OPERATIONS.md) for the full Linux installation guide.
