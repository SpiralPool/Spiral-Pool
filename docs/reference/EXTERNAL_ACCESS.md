# External Access Configuration

This guide explains how to expose your Spiral Stratum pool to external miners, such as hashrate marketplace rentals or other remote mining operations.

## Server Resource Requirements

Before renting hashpower or accepting external miners, ensure your server meets the minimum requirements for your target scale. Underpowered servers will drop connections, lose shares, and frustrate miners.

### Resource Tiers

| Tier | Connections | Shares/sec | CPU | RAM | Notes |
|------|-------------|------------|-----|-----|-------|
| **Small** | ≤500 | ~100/sec | 2 cores | 4 GB | Home lab, testing |
| **Medium** | ≤2,000 | ~500/sec | 4 cores | 8 GB | VPS, small pool |
| **Large** | ≤5,000 | ~2,000/sec | 8 cores | 16 GB | Production pool |
| **XL** | 10,000+ | ~5,000+/sec | 16+ cores | 32 GB+ | Dedicated host |

### Understanding the Metrics

**Connections:** The number of simultaneous TCP connections (miner devices) your server can handle.

**Shares/sec:** The total share submission rate across all miners. This is what actually loads the server - not hashpower directly.

### Hashpower to Share Rate

The relationship between hashpower and share rate depends on your pool's **vardiff (variable difficulty)** settings. Pools typically target 1 share every 5-15 seconds per miner.

**Approximate share rates at typical pool difficulties:**

| Algorithm | Hashpower | Approximate Shares/sec | Tier Needed |
|-----------|-----------|------------------------|-------------|
| SHA256 | 10 PH/s | ~200-500/sec | Medium |
| SHA256 | 50 PH/s | ~1,000-2,500/sec | Large |
| SHA256 | 100 PH/s | ~2,000-5,000/sec | Large/XL |
| SHA256 | 500 PH/s | ~10,000+/sec | XL+ |
| Scrypt | 100 GH/s | ~200-500/sec | Medium |
| Scrypt | 500 GH/s | ~1,000-2,500/sec | Large |
| Scrypt | 1 TH/s | ~2,000-5,000/sec | Large/XL |

**Note:** These are rough estimates. Actual rates depend on your vardiff configuration. Check `spiralctl pool stats` to monitor actual share rates.

### Choosing Your Tier

| Use Case | Recommended Tier |
|----------|-----------------|
| Testing external access | Small |
| Small hashrate marketplace orders (<10 PH/s SHA256) | Small/Medium |
| Medium hashrate marketplace orders (10-50 PH/s SHA256) | Medium/Large |
| Large hashrate marketplace orders (50-100 PH/s SHA256) | Large |
| Very large operations (100+ PH/s SHA256) | XL |

### Storage Requirements

External access does not significantly increase storage requirements. However, with more miners you will generate more logs:

| Tier | Log Storage (per day) | Recommended Disk |
|------|----------------------|------------------|
| Small | ~100 MB | 20 GB SSD |
| Medium | ~500 MB | 50 GB SSD |
| Large | ~2 GB | 100 GB SSD |
| XL | ~5 GB | 200+ GB SSD |

### Network Requirements

| Tier | Bandwidth | Connections |
|------|-----------|-------------|
| Small | 10 Mbps | ~1,000 TCP |
| Medium | 50 Mbps | ~4,000 TCP |
| Large | 100 Mbps | ~10,000 TCP |
| XL | 1 Gbps | ~20,000+ TCP |

**IPv4 only.** Spiral Pool does not support IPv6. The installer disables IPv6 at the OS level via sysctl. All stratum, API, and daemon connections use IPv4.

**Important:** Cloudflare Tunnel mode adds ~20-50ms latency but provides DDoS protection. For latency-sensitive operations at XL scale, use Port Forward mode with your own DDoS mitigation.

---

## Overview

The `spiralctl external` command provides two methods for exposing your pool to the internet:

1. **Port Forward Mode** - Traditional port forwarding through your router/firewall
2. **Cloudflare Tunnel Mode** - NAT traversal via Cloudflare's network (no router config needed)

## Quick Start

```bash
# Interactive setup wizard
spiralctl external setup

# Enable external access
spiralctl external enable

# Check status
spiralctl external status

# Test connectivity
spiralctl external test

# Disable when not needed
spiralctl external disable
```

## Port Forward Mode

Use this mode if you have access to configure your router and a static IP or dynamic DNS hostname.

### Prerequisites

- Router access for port forwarding configuration
- One of the following for your public address:
  - **Static IP address** (e.g., `X.X.X.X`)
  - **Dynamic DNS hostname** (e.g., `mypool.duckdns.org`, `pool.no-ip.org`)
  - **Domain name** pointing to your IP (e.g., `stratum.example.com`)
- Firewall rules allowing inbound TCP on your stratum port

### Setup Steps

1. Run the setup wizard:
   ```bash
   spiralctl external setup --mode port-forward
   ```

2. Enter your public hostname or IP address (either works):
   - Domain: `mypool.duckdns.org` or `stratum.example.com`
   - IP address: `X.X.X.X`

3. Configure port forwarding on your router:
   - External port: Your stratum port (e.g., 3333, 3335, etc.)
   - Internal IP: Your server's local IP
   - Internal port: Same as external (e.g., 3333)
   - Protocol: TCP

4. Enable external access:
   ```bash
   spiralctl external enable
   ```

**Important:** The port you forward must match the stratum port your miners connect to. If mining Bitcoin on port 4333, forward port 4333. If mining Litecoin on port 7333, forward port 7333.

### Router Configuration Examples

Replace `[PORT]` with your stratum port (e.g., 4333 for Bitcoin, 7333 for Litecoin). See [REFERENCE.md](REFERENCE.md) for all coin ports.

#### Generic Router
1. Access router admin (usually http://192.168.1.1)
2. Find "Port Forwarding", "NAT", or "Virtual Servers"
3. Add a new rule:
   - Service Name: Spiral Pool Stratum
   - External Port: [PORT]
   - Internal IP: [Your Server IP]
   - Internal Port: [PORT]
   - Protocol: TCP

**For multiple coins:** Add a separate rule for each stratum port.

#### pfSense
1. Navigate to Firewall > NAT > Port Forward
2. Add new rule:
   - Interface: WAN
   - Protocol: TCP
   - Destination Port Range: [PORT]
   - Redirect Target IP: [Your Server IP]
   - Redirect Target Port: [PORT]

#### OPNsense
1. Navigate to Firewall > NAT > Port Forward
2. Add new rule similar to pfSense

#### Example: Multi-Coin Port Forwarding

If running Bitcoin (4333), Litecoin (7333), and Dogecoin (8335):

| Service Name    | External Port | Internal Port | Protocol |
|-----------------|---------------|---------------|----------|
| SpiralPool-BTC  | 4333          | 4333          | TCP      |
| SpiralPool-LTC  | 7333          | 7333          | TCP      |
| SpiralPool-DOGE | 8335          | 8335          | TCP      |

## Cloudflare Tunnel Mode

> **Cloudflare Tunnel usage is subject to [Cloudflare's Terms of Service](https://www.cloudflare.com/terms/).** Operators are responsible for ensuring their use of Cloudflare Tunnel — including bandwidth consumption and traffic type — complies with Cloudflare's acceptable use policies and any applicable plan limits. The authors of this software are not affiliated with Cloudflare and make no representations regarding the permissibility of routing mining stratum traffic through their network.

Use this mode if you cannot configure port forwarding, are behind CGNAT, or want additional DDoS protection.

> **Requires Cloudflare Spectrum (paid add-on).** Standard Cloudflare Tunnels only proxy HTTP/WebSocket traffic. Mining hardware (ASICs, FPGAs) connects via raw TCP stratum protocol and cannot run `cloudflared` on the client side. Without Cloudflare Spectrum for raw TCP proxying, miners will **not** be able to connect through the tunnel. If you don't have Spectrum, use port-forward mode instead. The setup wizard will verify this during configuration.

### Prerequisites

1. Cloudflare account with **Spectrum** enabled (raw TCP proxying)
2. Domain name pointed to Cloudflare nameservers
3. `cloudflared` binary installed

### Install cloudflared

```bash
# Download the latest release
curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/local/bin/cloudflared

# Make it executable
chmod +x /usr/local/bin/cloudflared

# Verify installation
cloudflared version
```

### Create a Tunnel

1. Authenticate with Cloudflare:
   ```bash
   cloudflared tunnel login
   ```

2. Create a named tunnel:
   ```bash
   cloudflared tunnel create spiralpool-stratum
   ```

3. Note the tunnel ID and credentials file path

4. Configure DNS in Cloudflare dashboard:
   - Add a CNAME record: `stratum.yourdomain.com` -> `[tunnel-id].cfargotunnel.com`

### Setup with spiralctl

```bash
# Run the setup wizard
spiralctl external setup --mode tunnel

# Enter your tunnel name and hostname when prompted

# Enable external access
spiralctl external enable
```

The setup wizard will:
- Verify cloudflared installation
- Check tunnel exists
- Find credentials file
- Generate tunnel configuration
- Install systemd service for cloudflared

### Understanding Tunnel Ports

With Cloudflare Tunnel, miners always connect on **port 443** (the standard HTTPS port). The tunnel routes traffic to your local stratum port internally.

```
Miner connects to:     stratum+tcp://stratum.mypool.com:443
Tunnel routes to:      localhost:3333 (your local stratum port)
```

**For multiple coins:** Create a separate tunnel hostname for each stratum port:

| Tunnel Hostname | Routes To | Miner URL |
|-----------------|-----------|-----------|
| `btc.mypool.com`  | `localhost:4333` | `stratum+tcp://btc.mypool.com:443`  |
| `ltc.mypool.com`  | `localhost:7333` | `stratum+tcp://ltc.mypool.com:443`  |
| `doge.mypool.com` | `localhost:8335` | `stratum+tcp://doge.mypool.com:443` |

## Security Hardening

When external access is enabled, security hardening is automatically applied:

| Setting | Normal | Hardened (default) |
|---------|--------|--------------------|
| Connections per IP | 100 | 50 |
| Shares per second per IP | 100 | Configurable (see below) |
| Ban threshold | 10 | 5 |
| Ban duration | 30m | 60m |

These values help protect against:
- Connection flooding from single IPs
- Share spam attacks
- Repeated invalid share submissions

### Shares Per Second Configuration

The `sharesPerSecond` limit is configurable during setup because the correct value depends on how much rented hashrate you expect. Hashrate rental services (NiceHash, MiningRigRentals) often route traffic through proxy IPs that aggregate many workers — a single IP can submit hundreds or thousands of shares per second at scale.

The setup wizard (`spiralctl external setup`) prompts you to select a tier:

| Tier | Expected Hashrate | Shares/sec per IP | Use Case |
|------|-------------------|-------------------|----------|
| Small | <10 TH/s | 200 | Home miners, small rentals |
| Medium (default) | 10-100 TH/s | 500 | Moderate rentals |
| Large | 100 TH/s - 50 PH/s | 1000 | Large rentals, proxy aggregation |
| XL | 50+ PH/s | 2000 | Massive rentals, multiple proxies |
| Custom | Any | 10-100000 | Manual tuning |

**If miners are being rate-limited or banned**, increase this value by re-running:
```bash
spiralctl external setup
```

Monitor actual share rates with:
```bash
spiralctl pool stats
```

To disable automatic hardening entirely during setup:
```bash
# When prompted "Apply security hardening?" choose 'n'
```

## Hashrate Marketplace Configuration

To use your pool with a hashrate marketplace:

1. Enable external access:
   ```bash
   spiralctl external enable
   ```

2. Get your public endpoint:
   ```bash
   spiralctl external status
   ```

3. In hashrate marketplace:
   - Go to Marketplace > Create New Order
   - Select your algorithm (SHA256, Scrypt, etc.)
   - Enter your pool URL (see format below)
   - Enter your wallet address as the username
   - Password can be anything (e.g., `x`)

### Hashrate Marketplace Sizing Guide

Before placing an order, match your server tier to the hashpower you're renting. The key metric is **shares per second**, not raw hashpower.

| Algorithm | Small Tier | Medium Tier | Large Tier | XL Tier |
|-----------|------------|-------------|------------|---------|
| SHA256 | <5 PH/s | 5-20 PH/s | 20-75 PH/s | 75+ PH/s |
| Scrypt | <50 GH/s | 50-200 GH/s | 200-750 GH/s | 750+ GH/s |

**Example hashrate marketplace Order Sizing:**

| Your Server | Safe Order Size (SHA256) | Safe Order Size (Scrypt) |
|-------------|--------------------------|--------------------------|
| 2 cores / 4 GB | ≤5 PH/s | ≤50 GH/s |
| 4 cores / 8 GB | ≤20 PH/s | ≤200 GH/s |
| 8 cores / 16 GB | ≤75 PH/s | ≤750 GH/s |
| 16+ cores / 32 GB | 100+ PH/s | 1+ TH/s |

**Important:** These estimates assume typical vardiff settings. If you're seeing share rates higher than expected, your pool difficulty may be set too low. Monitor with `spiralctl pool stats`.

**Warning:** Ordering more hashpower than your server can handle will result in:
- Rejected shares (lost money)
- Connection timeouts
- Miner disconnections
- Poor reputation on hashrate marketplace

**Start small:** Place a test order at 10% of your tier's capacity first. Monitor share rates and server load. Scale up gradually.

### Pool URL Format

The pool URL format is:
```
stratum+tcp://<hostname-or-ip>:<port>
```

**Hostname:** Can be either a domain name OR an IP address:
- Domain name: `mypool.duckdns.org`, `stratum.example.com`
- IP address: `203.0.113.50`, `192.168.1.100` (if publicly routable)

**Port:** Depends on your access mode:

| Mode | Port | Notes |
|------|------|-------|
| Port Forward | Your stratum port (e.g., 3333) | Whatever port you configured for miners |
| Cloudflare Tunnel | 443 | Always 443 - CF tunnels use HTTPS port |

### Example Pool URLs

**Port Forward mode** (using your stratum port):
```
stratum+tcp://mypool.duckdns.org:3333
stratum+tcp://203.0.113.50:3333
stratum+tcp://pool.example.com:4444
```

**Cloudflare Tunnel mode** (always port 443):
```
stratum+tcp://stratum.mydomain.com:443
stratum+tcp://btc.mypool.com:443
```

### Coin-Specific Stratum Ports

If you're running multiple coins, each typically uses a different stratum port. Use the port for the specific coin you're renting hashpower for:

| Coin              | Algorithm | Stratum Port | Example URL (Port Forward)          |
|-------------------|-----------|--------------|-------------------------------------|
| DigiByte (SHA-256d) | SHA-256d  | 3333         | `stratum+tcp://mypool.com:3333`     |
| DigiByte (Scrypt) | Scrypt    | 3336         | `stratum+tcp://mypool.com:3336`     |
| Bitcoin           | SHA-256d  | 4333         | `stratum+tcp://mypool.com:4333`     |
| Bitcoin Cash      | SHA-256d  | 5333         | `stratum+tcp://mypool.com:5333`     |
| Bitcoin II        | SHA-256d  | 6333         | `stratum+tcp://mypool.com:6333`     |
| Litecoin          | Scrypt    | 7333         | `stratum+tcp://mypool.com:7333`     |
| Dogecoin          | Scrypt    | 8335         | `stratum+tcp://mypool.com:8335`     |
| PepeCoin          | Scrypt    | 10335        | `stratum+tcp://mypool.com:10335`    |
| Catcoin           | Scrypt    | 12335        | `stratum+tcp://mypool.com:12335`    |
| Namecoin          | SHA-256d  | 14335        | `stratum+tcp://mypool.com:14335`    |
| Syscoin           | SHA-256d  | 15335        | `stratum+tcp://mypool.com:15335`    |
| Myriad            | SHA-256d  | 17335        | `stratum+tcp://mypool.com:17335`    |
| Fractal Bitcoin   | SHA-256d  | 18335        | `stratum+tcp://mypool.com:18335`    |
| Q-BitX            | SHA-256d  | 20335        | `stratum+tcp://mypool.com:20335`    |

**Note:** These are example ports. Use whatever ports you configured in your pool. Check `spiralctl status` to see your actual stratum ports.

**For Cloudflare Tunnel:** You need a separate tunnel hostname for each coin/port:
```
stratum+tcp://btc.mypool.com:443    # Routes to local :4333
stratum+tcp://ltc.mypool.com:443    # Routes to local :7333
stratum+tcp://doge.mypool.com:443   # Routes to local :8335
```

### Hashrate Marketplace Best Practices

1. **Start small**: Begin with a small order to test connectivity
2. **Monitor closely**: Watch `spiralctl external status` during first orders
3. **Scale gradually**: Increase order size only after confirming stability
4. **Use fixed orders**: Avoid "standard" orders until you verify capacity
5. **Set limits**: Configure minimum limit per order to prevent spikes

## Monitoring

### Check Status

```bash
spiralctl external status
```

Output includes:
- Current mode and status
- Public endpoint
- Tunnel process status (for tunnel mode)
- Security hardening status

### Test Connectivity

```bash
spiralctl external test
```

This will:
1. Resolve DNS for your hostname
2. Test TCP connectivity
3. Verify port is reachable from external services

### View Tunnel Logs (Cloudflare mode)

```bash
journalctl -u cloudflared-spiralpool -f
```

## Troubleshooting

### Port Forward Mode

**Port appears closed:**
- Verify port forwarding rule is active in router
- Check firewall allows inbound connections (e.g., `ufw allow 3333/tcp`)
- Ensure your ISP doesn't block incoming connections
- Try a different port (some ISPs block common ports)

**DNS not resolving:**
- Verify DDNS is updating correctly
- Check DNS propagation (can take up to 48 hours)
- Use IP address directly to test

### Cloudflare Tunnel Mode

**Tunnel not starting:**
- Check credentials file exists and is readable
- Verify tunnel name matches exactly
- Check cloudflared logs: `journalctl -u cloudflared-spiralpool`

**Connection refused:**
- Verify local stratum server is running on the configured port
- Check tunnel configuration matches your stratum port

**DNS not resolving:**
- Verify CNAME record is configured in Cloudflare
- Ensure tunnel ID matches the CNAME target

### General Issues

**High latency:**
- For Cloudflare tunnels, latency is typically 20-50ms higher
- This is normal and acceptable for mining
- Use port forwarding if lowest latency is critical

**Connection drops:**
- Enable keepalive in your stratum configuration
- Check for network stability issues
- Monitor server resources (CPU, memory, disk)

## FAQ

**Q: Which mode should I choose?**
A: Use Port Forward if you have a static IP and can configure your router. Use Cloudflare Tunnel if you're behind CGNAT, can't access your router, or want additional DDoS protection.

**Q: Can I use an IP address instead of a domain name?**
A: Yes, for Port Forward mode you can use either a domain name (e.g., `mypool.duckdns.org`) or a public IP address (e.g., `203.0.113.50`). Cloudflare Tunnel mode requires a domain name.

**Q: What port do miners connect to?**
A: For Port Forward mode, miners connect to whatever stratum port you configured (e.g., 4333 for Bitcoin, 7333 for Litecoin). For Cloudflare Tunnel mode, miners always connect on port 443 - the tunnel routes to your local stratum port internally.

**Q: Is there additional cost for Cloudflare Tunnel?**
A: No, Cloudflare Tunnel is free for personal use. Enterprise features require a paid plan.

**Q: Will external access affect my local miners?**
A: No, local miners continue to connect to your local stratum port. External access creates an additional path for external connections.

**Q: Can I use both modes simultaneously?**
A: The current implementation supports one mode at a time. You could manually configure multiple access methods, but spiralctl manages only one.

**Q: How do I expose multiple coins (multiple stratum ports)?**
A: For Port Forward mode, forward each stratum port separately in your router. For Cloudflare Tunnel, create separate tunnel hostnames for each coin (e.g., `btc.mypool.com`, `ltc.mypool.com`).

**Q: How do I update my configuration?**
A: Run `spiralctl external setup` again to reconfigure. Then run `spiralctl external disable` followed by `spiralctl external enable` to apply changes.

## Security Considerations

Exposing your pool to the internet increases attack surface. Recommendations:

1. **Enable security hardening** (default)
2. **Monitor connection patterns** for unusual activity
3. **Keep software updated** including cloudflared
4. **Use TLS** if your stratum server supports it
5. **Implement IP allowlisting** if you know your miners' IPs
6. **Set up monitoring alerts** for unusual traffic patterns

## Commands Reference

| Command | Description |
|---------|-------------|
| `spiralctl external setup` | Interactive configuration wizard |
| `spiralctl external setup --mode port-forward` | Configure port forward mode |
| `spiralctl external setup --mode tunnel` | Configure tunnel mode |
| `spiralctl external enable` | Enable external access |
| `spiralctl external disable` | Disable external access |
| `spiralctl external status` | Show current configuration and status |
| `spiralctl external test` | Test external connectivity |

## Configuration File

External access configuration is stored in:
```
/etc/spiralpool/external.yaml
```

This file is managed by spiralctl and should not be edited directly.
