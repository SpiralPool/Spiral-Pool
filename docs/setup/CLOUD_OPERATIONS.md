# Spiral Pool Cloud Operations Guide

Running Spiral Pool on a VPS or cloud provider requires different network and security decisions than a home or bare-metal install. This document covers everything specific to cloud deployments.

> **IMPORTANT**: This software has NOT been audited by third-party security professionals. The guidance here represents operational best practices, not security guarantees. Operators are solely responsible for their own security posture. See [SECURITY.md](../../SECURITY.md) for vulnerability reporting.

---

## Contents

- [Provider Terms of Service Warning](#provider-terms-of-service-warning)
- [Bandwidth and Billing Warning](#bandwidth-and-billing-warning)
- [IPv6 Disabled at the Kernel Level](#ipv6-disabled-at-the-kernel-level)
- [High Availability Not Supported](#high-availability-not-supported)
- [How Cloud Installs Differ](#how-cloud-installs-differ)
- [Dashboard Access via SSH Tunnel](#dashboard-access-via-ssh-tunnel)
- [Firewall (UFW) Layout](#firewall-ufw-layout)
- [Exposed Ports](#exposed-ports)
- [SSH Hardening](#ssh-hardening)
- [Fail2Ban](#fail2ban)
- [API Security](#api-security)
- [Stratum on the Public Internet](#stratum-on-the-public-internet)
- [Prometheus Metrics](#prometheus-metrics)
- [Wallet Security on Cloud](#wallet-security-on-cloud)
- [ZMQ and RPC Port Security](#zmq-and-rpc-port-security)
- [Credentials Security on Cloud](#credentials-security-on-cloud)
- [Swap Security](#swap-security)
- [Automatic Reboots](#automatic-reboots)
- [PostgreSQL Data Durability](#postgresql-data-durability)
- [No HTTPS — Design Decision](#no-https--design-decision)
- [Recommended Provider Configuration](#recommended-provider-configuration)
- [Post-Install Checklist](#post-install-checklist)

---

## Provider Terms of Service Warning

> **WARNING: Running a cryptocurrency mining pool may violate your cloud provider's Acceptable Use Policy (AUP) or Terms of Service (ToS).**

Most major cloud providers explicitly prohibit cryptocurrency mining or mining-related workloads. This includes (but is not limited to):

| Provider | AUP/ToS Stance |
|----------|---------------|
| AWS | Prohibits activities that interfere with or disrupt AWS services; mining has been cited in enforcement actions |
| Google Cloud | Explicitly prohibits cryptocurrency mining |
| Microsoft Azure | Explicitly prohibits cryptocurrency mining |
| DigitalOcean | Prohibits cryptocurrency mining |
| Vultr | Prohibits cryptocurrency mining |
| Hetzner | Prohibits cryptocurrency mining in their server/VPS products |
| OVHcloud | Prohibits activities causing excessive resource use; mining enforcement is active |
| Linode/Akamai | Prohibits cryptocurrency mining |

> **Note:** Even if your ASIC miners are physically elsewhere and only connecting to the pool remotely, operating the stratum server, blockchain node, and block submission infrastructure on a cloud instance is what constitutes "mining" for ToS purposes.

**Consequences of ToS violation:**

- **Immediate account termination without notice** — your instance is deleted
- **All data permanently lost** — no recovery, no grace period
- **Forfeiture of prepaid balance or credits**
- **Potential civil or legal action** depending on the provider's specific terms

**You are solely responsible** for reading your provider's Terms of Service and Acceptable Use Policy before deploying. Spiral Pool and its contributors accept no liability for account terminations, data loss, or legal consequences arising from ToS violations.

The installer requires you to explicitly acknowledge this risk by typing `YES` before installation proceeds on a detected cloud provider.

---

## Bandwidth and Billing Warning

> **WARNING: Blockchain synchronization and ongoing pool traffic can generate large, unexpected bandwidth charges on cloud infrastructure.**

Cloud providers charge for outbound (egress) network traffic. Blockchain sync pulls hundreds of gigabytes of data and pushes headers and transactions to peers, generating egress charges that begin from the moment the daemon starts.

### Estimated initial sync egress by coin

| Coin | Blockchain Size | Estimated Egress | Cost at $0.09/GB |
|------|----------------|------------------|-----------------|
| Bitcoin (BTC) | ~600 GB | ~600 GB | ~$54 |
| Bitcoin Cash (BCH) | ~250 GB | ~250 GB | ~$23 |
| Litecoin (LTC) | ~120 GB | ~120 GB | ~$11 |
| Dogecoin (DOGE) | ~90 GB | ~90 GB | ~$8 |
| DigiByte (DGB) | ~45 GB | ~45 GB | ~$4 |
| QBitX (QBX) | ~5 GB | ~5 GB | <$1 |

> Blockchain sizes grow over time. The figures above are approximate as of Q1 2026.

Multi-coin deployments multiply these costs. Ongoing P2P traffic (mempool propagation, peer keepalive, IBD catch-up) adds continuous egress after sync completes — typically 1–10 GB/day depending on coin and peer count.

### Stratum traffic

Each miner connection generates stratum traffic. With many miners, outbound share acknowledgments and job broadcasts add measurable egress over time. This is minor compared to blockchain sync but ongoing.

### How to reduce costs

- **Use a provider with free or cheap egress**: Hetzner (EU) includes generous transfer quotas; Oracle Cloud Free Tier includes 10 TB/month outbound at no charge
- **Reduce peer count**: Lower `maxpeers` in your coin's daemon config to reduce P2P sync traffic
- **Check your provider's included transfer quota**: Many VPS providers (Hetzner, Vultr, Linode) include 1–5 TB/month in the instance price before per-GB charges begin
- **Set billing alerts**: Configure your provider's billing alert thresholds before sync starts

**You are solely responsible** for monitoring your cloud billing and understanding your provider's network pricing. Spiral Pool and its contributors accept no liability for unexpected billing charges.

The installer requires you to explicitly acknowledge bandwidth billing risk by typing `YES` before installation proceeds on a detected cloud provider.

---

## IPv6 Disabled at the Kernel Level

> **WARNING: This installer disables IPv6 system-wide at the kernel level. This is permanent until manually reversed and may cause loss of network connectivity if your environment depends on IPv6.**

Spiral Pool disables IPv6 via `sysctl` (`net.ipv6.conf.all.disable_ipv6 = 1`) because IPv6 causes kernel routing cache corruption during keepalived VIP failover operations in HA mode. This applies even on single-node installs to maintain a consistent configuration baseline.

### What gets disabled

- All IPv6 network interfaces on the server stop responding
- IPv6 DNS resolution from this server stops working
- Any IPv6 tunnels (6in4, Teredo, AYIYA, GRE6, etc.) break immediately
- Services bound to IPv6 addresses (`::1`, `::`, etc.) become unreachable

### When this is a problem

| Scenario | Risk |
|----------|------|
| Your cloud provider assigns only an IPv6 address to this instance | **SSH access is lost immediately after reboot** |
| Your provider assigns a dual-stack address but you SSH in via IPv6 | SSH drops after reboot; reconnect via IPv4 |
| You have IPv6 tunnels configured (e.g. Hurricane Electric, Vultr /48) | Tunnels break |
| Other services on this machine use IPv6 listeners | Those services become unreachable |

### Before deploying on a cloud instance

1. Confirm your instance has a **static IPv4 address** and you have IPv4 SSH access
2. Confirm you do not rely on IPv6 tunnels for connectivity
3. Check your provider's network configuration panel — if the instance is IPv6-only, do not deploy Spiral Pool on it

### Reversing the change (if needed)

```bash
sudo sysctl -w net.ipv6.conf.all.disable_ipv6=0
sudo sysctl -w net.ipv6.conf.default.disable_ipv6=0
# Remove from /etc/sysctl.d/99-spiralpool.conf and reload:
sudo sysctl --system
```

> Note: Reversing this may break keepalived HA VIP failover if HA is enabled.

The installer requires you to explicitly acknowledge the IPv6 disablement risk by typing `YES` before installation proceeds on a detected cloud provider.

---

## High Availability Not Supported

> **High Availability (HA) cluster mode is not supported on cloud or VPS deployments. The installer enforces Standalone mode automatically.**

If you select HA Primary (option 2) or HA Backup (option 3) during installation on a detected cloud provider, the installer will reject your selection and automatically revert to Standalone (option 1). This cannot be overridden.

### Why HA does not work on cloud infrastructure

| Reason | Detail |
|--------|--------|
| VRRP/keepalived VIP | Most cloud providers block or filter VRRP multicast packets at the network layer. The virtual IP will not float between nodes — failover silently fails. |
| No physical node isolation | Cloud VMs share a hypervisor layer. A compromised or snapshotted hypervisor exposes all HA nodes simultaneously. |
| etcd split-brain risk | Cloud network latency is variable and provider-controlled. etcd quorum loss and split-brain conditions are more likely than on a private LAN. |
| Provider migration events | Cloud VMs can be live-migrated without notice, disrupting keepalived VRRP state and triggering false failovers. |
| Shared network boundary | etcd and Patroni control plane ports are difficult to isolate on cloud security groups when nodes are on the same provider VLAN. |

### If you need redundancy on cloud

Cloud providers offer their own managed redundancy products that are better suited to this environment:

- **Load balancer + health checks** — route stratum traffic to a healthy instance
- **Managed database** (AWS RDS, GCP Cloud SQL, Azure Database) — offload PostgreSQL HA to the provider
- **Instance auto-recovery** — most providers can auto-restart instances on hardware failure

These are out of scope for Spiral Pool. HA on bare metal with your own hardware remains fully supported.

---

## How Cloud Installs Differ

| Concern | Home Install | Cloud/VPS Install |
|---------|-------------|-------------------|
| Dashboard port 1618 | Open on LAN | **Closed — SSH tunnel only** |
| SSH access | Open to all | Restricted to operator IP |
| Bot traffic on stratum | Minimal | Constant — fail2ban is active |
| DDoS risk | Low | Real — stratum ports are public |
| Dashboard encryption | LAN traffic, acceptable | Would be HTTP over internet — unacceptable |
| High Availability (HA) | Supported | **Not supported — standalone only** |
| IPv6 | Enabled | **Disabled at kernel level** |

The installer automatically detects cloud providers (AWS, GCP, Azure, DigitalOcean, Hetzner, Vultr, OVH, and 20+ others) and applies cloud-specific firewall rules.

---

## Dashboard Access via SSH Tunnel

The dashboard runs on port 1618 over plain HTTP. On cloud installs, **this port is intentionally not opened** in the firewall. Exposing an unencrypted HTTP admin interface to the internet is unsafe regardless of password protection — credentials and session data travel in plaintext.

The correct access method is an SSH tunnel, which encrypts all traffic through your existing SSH session.

### Setting up the tunnel

From your local machine:

```bash
ssh -L 1618:localhost:1618 user@YOUR_SERVER_IP
```

Then open your browser to:

```
http://localhost:1618
```

Traffic flows: browser → localhost:1618 → SSH encryption → server:22 → localhost:1618 → gunicorn.

### Keeping the tunnel open persistently

To run the tunnel in the background without an interactive shell:

```bash
ssh -fNL 1618:localhost:1618 user@YOUR_SERVER_IP
```

`-f` backgrounds the process, `-N` tells SSH not to execute a remote command.

### SSH config shortcut (~/.ssh/config)

```
Host spiralpool
    HostName YOUR_SERVER_IP
    User spiralpool
    LocalForward 1618 localhost:1618
    ServerAliveInterval 60
```

Then just run:

```bash
ssh spiralpool
```

The tunnel opens automatically when you SSH in.

### Using a multiplexed connection (no extra terminal)

If you want the tunnel without keeping an SSH terminal open:

```bash
ssh -fNMS /tmp/spiralpool.sock -L 1618:localhost:1618 user@YOUR_SERVER_IP
# Close it later with:
ssh -S /tmp/spiralpool.sock -O exit user@YOUR_SERVER_IP
```

---

## Firewall (UFW) Layout

The installer configures UFW with these rules on cloud installs:

| Port | Protocol | Rule | Notes |
|------|----------|------|-------|
| 22 | TCP | `allow from OPERATOR_IP` | SSH — restricted to operator IP only |
| Stratum ports | TCP | `allow` | Open to all — miners need to connect |
| 4000 | TCP | `limit` | Pool API — rate-limited (6 conn/30s per IP) |
| 1618 | — | **not opened** | Dashboard — SSH tunnel only |
| 9100 | TCP | `allow from local_subnet` | Prometheus — LAN/loopback only |

Default policy: `deny incoming`, `allow outgoing`.

### Adding additional operator IPs

If you need to access the pool from multiple IPs (e.g. office and home):

```bash
sudo ufw allow from YOUR_OTHER_IP to any port 22 proto tcp
```

### Opening the API to a specific external monitoring IP

If you run an external Prometheus scraper:

```bash
sudo ufw delete limit 4000/tcp
sudo ufw allow from YOUR_PROMETHEUS_IP to any port 4000 proto tcp
```

---

## Exposed Ports

Only stratum ports are open to the public internet. This is unavoidable — miners worldwide need to connect. Spiral Pool's built-in protection on these ports includes:

- **Connection limit**: 200 concurrent connections per source IP per port (iptables `connlimit`)
- **Fail2ban**: Monitors authentication failures and bans abusive IPs
- **FSM enforcement**: Miners cannot submit shares before subscribing and authorizing (out-of-order messages are rejected)
- **Pre-auth limits**: Connection rate limiting before a miner authenticates
- **Ban persistence**: Bans survive service restarts via `/spiralpool/data/bans.json`

See [SECURITY_MODEL.md](../architecture/SECURITY_MODEL.md) for detailed values.

---

## SSH Hardening

The installer restricts SSH to your operator IP, but further hardening is recommended:

### Disable password authentication

```bash
sudo nano /etc/ssh/sshd_config
```

Set:
```
PasswordAuthentication no
PubkeyAuthentication yes
PermitRootLogin no
```

Then:
```bash
sudo systemctl reload sshd
```

> Do this **only after** confirming your SSH key works. Locking yourself out requires console access through your VPS provider.

### Use SSH keys

Generate a key if you don't have one:
```bash
ssh-keygen -t ed25519 -C "spiralpool-admin"
```

Copy it to the server:
```bash
ssh-copy-id user@YOUR_SERVER_IP
```

### Change the SSH port (optional)

If you change SSH away from port 22, update UFW:

```bash
sudo ufw allow from YOUR_OPERATOR_IP to any port NEW_PORT proto tcp
sudo ufw delete allow from YOUR_OPERATOR_IP to any port 22 proto tcp
```

And update `/etc/ssh/sshd_config`:
```
Port NEW_PORT
```

---

## Fail2Ban

Fail2ban is installed and configured by the installer. It monitors:

- SSH login failures (`/var/log/auth.log`)
- Stratum authentication failures (custom filter on stratum logs)
- Prometheus metrics abuse (custom filter on kernel LOG lines)

### Check current bans

```bash
sudo fail2ban-client status
sudo fail2ban-client status sshd
sudo fail2ban-client status spiral-stratum
```

### Unban an IP

```bash
sudo fail2ban-client set sshd unbanip IP_ADDRESS
```

### View fail2ban logs

```bash
sudo journalctl -u fail2ban --since "1 hour ago"
```

---

## API Security

The pool API (port 4000) is rate-limited but open to the public. It serves read-only pool statistics that miners use to check their share counts and balances.

The API has two access tiers:

- **Public endpoints** (`/api/pools`, `/api/pools/{id}/miners/{address}`, etc.) — no auth required
- **Admin endpoints** — require the admin API key from `/spiralpool/config/sentinel/config.json`

Do not expose your admin API key. It is stored in the sentinel config and used only by the dashboard and sentinel internally.

### Restricting the API to specific IPs

If your pool is private (miners are known, trusted parties):

```bash
sudo ufw delete limit 4000/tcp
for IP in MINER_IP_1 MINER_IP_2 YOUR_OPERATOR_IP; do
    sudo ufw allow from $IP to any port 4000 proto tcp
done
sudo ufw reload
```

---

## Stratum on the Public Internet

Stratum ports must be reachable by your miners. If your miners are in a fixed location (e.g., a home farm connecting to a cloud pool), you can restrict stratum to those IPs:

```bash
# Remove the open allow rule for a stratum port
sudo ufw delete allow 3333/tcp

# Replace with specific source IPs
sudo ufw allow from YOUR_FARM_IP to any port 3333 proto tcp
sudo ufw reload
```

This eliminates all bot traffic on stratum ports entirely.

### DDoS considerations

Cloud providers typically offer network-level DDoS protection. Enable it on your VPS if available (most providers include basic protection at no cost). This operates upstream of UFW and handles volumetric attacks before they reach the server.

For severe targeted attacks, consider:

1. **Port migration** — change stratum to a non-standard port and update miner configs
2. **VPN-gated stratum** — run WireGuard and restrict stratum to VPN clients only
3. **Provider support** — open a ticket with your VPS provider; they can apply upstream ACLs

---

## Prometheus Metrics

On cloud installs, the metrics endpoint (port 9100) is restricted to **loopback only** (`127.0.0.1`/`::1`). The "local subnet" on a cloud provider's network may include other customers' VMs — it is never opened to the cloud internal network.

The metrics endpoint also requires a bearer token (`SPIRAL_METRICS_TOKEN`) which is auto-generated during installation. This token is enforced automatically on cloud installs.

If you run an external Prometheus scraper on a specific trusted IP, add it explicitly and ensure the token is configured in your Prometheus scrape config:

```bash
sudo ufw allow from YOUR_PROMETHEUS_SERVER_IP to any port 9100 proto tcp
```

The metrics endpoint has iptables hashlimit rate limiting (120 requests/min per IP, burst 20) independent of UFW.

---

## Wallet Security on Cloud

> **This is one of the most important cloud-specific risks. Read this before installation.**

### How Spiral Pool handles wallet addresses

Spiral Pool is non-custodial. Block rewards flow directly from the blockchain to the coinbase address embedded in the block template — the pool server never holds, receives, or transmits funds. You provide a payout address; the stratum binary encodes it into every block template it constructs.

At runtime, **no wallet RPC calls are made**. The stratum binary uses the static address from config. Wallets are only involved during installation if you choose to auto-generate an address.

### The cloud risk: auto-generated wallets create private keys on the server

During installation, each coin prompts:

```
[1] I have a wallet — Use an existing address
[2] Generate one for me — Create a wallet on this server
```

If you choose **[2]**, the coin daemon creates a `wallet.dat` file on the server's disk containing your **unencrypted private keys**:

```
~spiraluser/.bitcoin/wallets/pool-btc/wallet.dat
~spiraluser/.litecoin/wallets/pool-ltc/wallet.dat
~spiraluser/.digibyte/wallets/pool-dgb/wallet.dat
(and equivalent paths for each enabled coin)
```

These files have `chmod 600` (root/spiraluser-readable only), but **file permissions do not protect against cloud provider disk access**. Your provider can:

- Take a disk snapshot that includes all wallet.dat files
- Live-migrate the VM with memory and disk contents intact
- Access disk contents via hypervisor at any time

If your cloud account is compromised or your provider is subpoenaed, your private keys are exposed.

### Strongly recommended: use option [1] for all coins on cloud

Provide addresses from a **hardware wallet** (Ledger, Trezor, Coldcard) or an **air-gapped wallet** generated offline. In this case:

- No `wallet.dat` is created on the server
- No private keys ever exist on the cloud VPS
- Block rewards still go directly to your address on-chain
- The pool server has zero access to your funds

### If you must auto-generate on cloud

After blockchain sync completes and the wallet is created:

1. Export your keys to an offline location:
   ```bash
   sudo spiralctl wallet export
   ```
2. Store the exported keys in a hardware wallet or encrypted offline backup
3. Delete the wallet.dat from the server:
   ```bash
   sudo spiralctl wallet purge
   ```

> **Note:** After purging wallet.dat, the daemon can no longer generate new addresses for that coin. The existing coinbase address in config continues to work — block rewards still go to it. Only the private keys are removed from the server.

### Daemon wallet status

All coin daemons run with `disablewallet=0` — wallet functionality is enabled. This is required to support the "generate" option during installation. If you used option [1] for all coins, the daemon's wallet is empty and presents no key exposure risk.

---

## ZMQ and RPC Port Security

### ZMQ (block notification ports)

Spiral Pool's coin daemons use ZMQ to notify the stratum binary of new blocks. All ZMQ sockets bind to `127.0.0.1` — they are never exposed on external interfaces. The ports (28332, 28432, 28532, 28555, 28933, etc.) are used exclusively for localhost inter-process communication between the daemon and stratum binary, both of which run on the same machine.

No UFW rules are needed for ZMQ because the OS kernel rejects all external connection attempts to a `127.0.0.1`-bound socket before they reach the application.

### RPC (daemon remote procedure call)

All coin daemons are configured with:

```
rpcallowip=127.0.0.1
```

This means daemon RPC access is restricted to localhost regardless of which interface the daemon is listening on. External RPC access is impossible by design. The stratum binary connects to each daemon via `http://127.0.0.1:PORT/` — no external RPC exposure exists.

**Tor does not affect this.** When Tor is enabled, it only routes the daemon's outbound P2P connections to other blockchain nodes. The local RPC connection between stratum and the daemon remains on `127.0.0.1` and is unaffected.

> Note: Tor is automatically disabled on cloud installs. See the [Provider Terms of Service Warning](#provider-terms-of-service-warning) — enabling Tor on a cloud VPS compounds the ToS violation and does not protect against provider-level access to your data.

---

## Credentials Security on Cloud

During installation, your admin API key, metrics token, RPC passwords, and database credentials are saved to:

```
/spiralpool/config/credentials.txt  (chmod 600 — root-readable only)
```

On a cloud VPS, your provider can snapshot or inspect disk contents at any time. After retrieving your credentials:

1. **Copy the admin API key to a secure offline location** (password manager)
2. **Delete the credentials file:**
   ```bash
   sudo rm /spiralpool/config/credentials.txt
   ```
3. **Clear your terminal history:**
   ```bash
   history -c && clear
   ```

To retrieve the admin API key later without the credentials file:

```bash
sudo grep admin_api_key /spiralpool/config/stratum/config.yaml
```

---

## Swap Security

The installer creates a 4 GB swapfile (`/swapfile`, `chmod 600`) to prevent out-of-memory kills during blockchain sync. On cloud VPS instances, this file lives on provider-managed disk.

**Risk:** Linux swaps memory pages to disk when RAM pressure is high. In-use credential data held in process memory (RPC passwords, database passwords, API keys) can be written to `/swapfile` and persist on the provider's physical disk after the data is no longer needed in RAM. A provider with disk-level access (via snapshot, forensic imaging, or hardware access) could potentially recover this data.

**Mitigations:**

| Action | Command |
|--------|---------|
| Use encrypted-disk VM | Available on some providers (AWS EBS encryption, GCP CMEK, etc.) — prefer this for sensitive workloads |
| Disable swap after sync completes | `sudo swapoff /swapfile && sudo sed -i '/swapfile/d' /etc/fstab` — only if you have sufficient RAM (16+ GB recommended) |
| Reduce swap exposure | Applications are single-process; swap is only written under memory pressure. Monitoring RAM usage with `free -h` shows when swap is actually in use. |

> For most operators with 8–16 GB RAM running 1–3 coins, swap is rarely written to under normal pool operation. Sync is the high-memory phase.

---

## Automatic Reboots

The installer configures `unattended-upgrades` to automatically install security updates and **reboot at 04:00 UTC** if a kernel or security patch requires it. This is enabled by default because staying on unpatched kernels on a public-internet server is significantly riskier than planned downtime.

**What happens during an auto-reboot:**

- Pool services (`spiralstratum`, `spiralsentinel`, `spiraldash`) stop gracefully via a pre-shutdown hook
- In-flight shares are flushed before shutdown
- The server reboots — ~1–2 minutes of downtime
- All services start automatically via systemd on boot

**Impact:**

- Connected miners will disconnect and reconnect automatically (all modern mining firmware handles reconnects)
- Shares submitted during the ~1–2 minute window are lost
- The event appears in your provider's console as a reboot

**To disable auto-reboot (and manage reboots yourself):**

```bash
sudo nano /etc/apt/apt.conf.d/50unattended-upgrades
# Change: Unattended-Upgrade::Automatic-Reboot "true";
# To:     Unattended-Upgrade::Automatic-Reboot "false";
```

If you disable auto-reboot, you are responsible for monitoring `needrestart` or `/var/run/reboot-required` and rebooting during planned maintenance windows.

> **Postgres and ZMQ packages are excluded from auto-upgrade** (`postgresql*`, `libpq*`) to avoid unexpected database version changes. Only OS security packages (kernel, glibc, openssl, etc.) are auto-upgraded.

---

## PostgreSQL Data Durability

Cloud providers can live-migrate, pause, snapshot, or suspend VMs without notice. This has implications for PostgreSQL data integrity:

| Event | Risk |
|-------|------|
| VM live-migrated mid-write | PostgreSQL WAL may need crash recovery on next start |
| Snapshot taken during active writes | Restoring from snapshot loses all data written since snapshot |
| VM paused/suspended during transaction | PostgreSQL crash recovery on resume — shares in flight may be lost |

### Mitigations

- **Enable automatic backups**: Run `sudo spiralctl data backup` on a schedule (via cron or your provider's snapshot feature) at a frequency you're comfortable losing data from
- **Set your provider's snapshot schedule**: Most providers (DigitalOcean, Linode, Hetzner) offer automated weekly/daily snapshots — enable them
- **Understand your RPO (Recovery Point Objective)**: How many shares/blocks are you willing to lose if you must restore from a backup? Set your backup frequency accordingly
- **Monitor replication lag**: If you ever run a second read replica for monitoring, watch replication lag — high lag indicates I/O pressure
- **Do not restore from snapshots without stopping services first**: Before restoring, stop Spiral Pool services to prevent PostgreSQL from receiving writes during restore:
  ```bash
  sudo systemctl stop spiralstratum spiralsentinel spiraldash
  ```

PostgreSQL's `fsync=on` (default) ensures data is durable to the virtual disk at the moment of commit. The risk is at the *provider infrastructure layer* — not within PostgreSQL itself.

---

## No HTTPS — Design Decision

Spiral Pool does not include a built-in HTTPS/TLS terminator for the dashboard or API. This is a deliberate scope decision — adding certificate management, renewal automation, and a reverse proxy layer to the installer would significantly increase complexity and failure modes for the majority of users who run on home networks.

For cloud operators who want HTTPS:

### Option 1: Nginx reverse proxy (recommended for public pools)

```bash
sudo apt install nginx certbot python3-certbot-nginx

# Create /etc/nginx/sites-available/spiralpool:
server {
    listen 443 ssl;
    server_name YOUR_DOMAIN;

    location / {
        proxy_pass http://127.0.0.1:1618;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

sudo certbot --nginx -d YOUR_DOMAIN
```

Then open 443 in UFW and optionally close 1618 to localhost only:
```bash
sudo ufw allow 443/tcp
```

### Option 2: Cloudflare Tunnel (no open ports needed)

Cloudflare Tunnel (`cloudflared`) creates an outbound-only connection from your server to Cloudflare's edge. Your dashboard becomes accessible at a Cloudflare-managed domain with automatic HTTPS, without opening any inbound ports.

See [EXTERNAL_ACCESS.md](../reference/EXTERNAL_ACCESS.md) for Cloudflare Tunnel setup.

### Option 3: SSH tunnel (recommended for private/personal pools)

The built-in approach described in this document. No infrastructure required, zero attack surface, traffic encrypted through SSH. Perfectly adequate for operators accessing their own pool.

---

## Recommended Provider Configuration

### DigitalOcean / Vultr / Linode / Hetzner

These providers offer a cloud firewall/security group that operates at the network level, upstream of the server. Set it to:

| Port(s) | Source | Action |
|---------|--------|--------|
| 22 | Your IP | Allow |
| 3333, 4333, etc. (stratum) | Anywhere | Allow |
| 4000 | Anywhere (or restrict) | Allow |
| All others | Anywhere | Deny |

This provides a second layer of protection. Even if UFW is misconfigured, the cloud firewall prevents unauthorized access.

### AWS EC2

Use a Security Group:

- Inbound: SSH from your IP, stratum ports from anywhere, API port from anywhere
- Outbound: All traffic (pool needs to communicate with blockchain peers)

Keep the default "deny all" inbound stance and add only what's needed.

### Oracle Cloud (OCI)

OCI has both a Security List and a Network Security Group. The installer adds UFW rules, but OCI's iptables rules (`iptables -L`) may also restrict traffic independently. If ports appear open in UFW but are unreachable, check:

```bash
sudo iptables -L INPUT -n --line-numbers
```

OCI's default image adds its own ACCEPT rules for ports 22, 80, 443. Add your stratum ports manually if blocked.

---

## Post-Install Checklist

After installing on a cloud VPS, verify:

- [ ] SSH key authentication works from your local machine
- [ ] SSH password authentication disabled in `/etc/ssh/sshd_config`
- [ ] `sudo ufw status` shows 1618 is NOT listed
- [ ] `sudo ufw status` shows SSH restricted to your operator IP
- [ ] `sudo ufw status` shows port 9100 allowed from `127.0.0.1` only (not subnet or any)
- [ ] SSH tunnel works: `ssh -L 1618:localhost:1618 user@SERVER` → `http://localhost:1618` loads dashboard
- [ ] Dashboard admin password set on first login
- [ ] Stratum port reachable from a test miner
- [ ] `sudo fail2ban-client status` shows jails are active
- [ ] Notifications (Discord/Telegram) configured in sentinel config
- [ ] Provider-level firewall/security group configured as second layer
- [ ] Admin API key copied to offline secure storage (password manager)
- [ ] Credentials file deleted: `sudo rm /spiralpool/config/credentials.txt`
- [ ] Terminal history cleared: `history -c`
- [ ] Provider snapshot/backup schedule configured
- [ ] Swap in use checked: `free -h` (if swap used frequently, consider encrypted disk or disabling after sync)
- [ ] Auto-reboot policy reviewed: decide if 04:00 UTC auto-reboot is acceptable or disable manually
- [ ] Tor is NOT enabled (verify: `sudo spiralctl config show | grep tor`)
- [ ] Wallet addresses came from a hardware wallet or air-gapped wallet (option [1] used for all coins)
- [ ] If option [2] was used for any coin: keys exported offline and `sudo spiralctl wallet purge` run to remove wallet.dat from server

---

*Spiral Pool — Phi Hash Reactor 2.1.0*
