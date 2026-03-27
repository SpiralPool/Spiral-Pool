# Coin Onboarding Specification

This document describes how to add support for new SHA-256d or Scrypt coins to Spiral Pool.

---

> **Important:** The onboarding process is mostly automated via `scripts/add-coin.py`, but the output **requires testing before production deployment**. Coin implementations can differ in subtle ways even among SHA256d forks — block header serialization, AuxPoW chain IDs, address encoding, version bits, and RPC behaviour all vary by fork. The generated Go code is a starting template, not a finished implementation.
>
> Before deploying any new coin:
> - Review the generated Go file and manifest entry carefully
> - Test against the coin's actual daemon (testnet/devnet if available)
> - Verify share submission, block template construction, and coinbase payout
> - Understand how the coin's consensus rules differ from Bitcoin/DigiByte
>
> AI assistance (Claude, GPT-4, etc.) is recommended for interpreting `chainparams.cpp`, understanding fork-specific differences, and debugging generated code — especially for unfamiliar or obscure coins.

---

## Overview

Adding a new coin requires two components:

1. **Manifest Entry** - YAML definition in `config/coins.manifest.yaml`
2. **Go Implementation** - Consensus logic in `src/stratum/internal/coin/<symbol>.go`

The manifest provides metadata (ports, display names, addresses). The Go code provides consensus-critical logic (hashing, serialization). Both are validated at startup.

The `scripts/add-coin.py` automation script handles most of steps 1 and 2 automatically when given a coin symbol and GitHub repository URL. Manual review and testing are always required after generation.

---

## Prerequisites

Before adding a coin, gather the following information:

| Field | Source | Example |
|-------|--------|---------|
| Symbol | Official ticker | `FBTC` |
| Algorithm | Chain documentation | `sha256d` or `scrypt` |
| Genesis Hash | `getblockhash 0` RPC call | `000000000019d6...` |
| Block Time | Chain documentation (seconds) | `30` |
| P2PKH Version | Source code or chainparams | `0x00` |
| P2SH Version | Source code or chainparams | `0x05` |
| Bech32 HRP | Source code (if supported) | `"bc"` |
| RPC Port | Default node configuration | `8332` |
| P2P Port | Default node configuration | `8333` |
| AuxPoW Support | Chain documentation | `true/false` |
| Chain ID | Source code (if AuxPoW) | `8228` |

---

## Step 1: Add Manifest Entry

Add the coin definition to `config/coins.manifest.yaml`:

```yaml
  - symbol: NEWCOIN
    name: New Coin Name
    algorithm: sha256d    # or scrypt
    role: standalone      # or parent, aux

    network:
      rpc_port: 8332
      p2p_port: 8333
      zmq_port: 28332

    mining:
      stratum_port: 19335       # Unique port (check existing coins)
      stratum_v2_port: 19336    # stratum_port + 1
      stratum_tls_port: 19337   # stratum_port + 2

    address:
      p2pkh_version: 0x00       # P2PKH address version byte
      p2sh_version: 0x05        # P2SH address version byte
      bech32_hrp: "nc"          # Bech32 prefix (or null if unsupported)

    chain:
      genesis_hash: "000000..."
      block_time: 600           # Block time in SECONDS
      coinbase_maturity: 100
      supports_segwit: true
      min_coinbase_script_len: 2

    display:
      full_name: "New Coin"
      short_name: "NC"
      coingecko_id: "new-coin"  # or null if not listed
      explorer_url: "https://explorer.example.com"
```

### For Merge-Mineable Coins (AuxPoW)

Add the `merge_mining` section:

```yaml
    merge_mining:
      supports_auxpow: true
      auxpow_start_height: 0      # Block height when AuxPoW activated
      chain_id: 1234              # CONSENSUS-CRITICAL - from source code
      version_bit: 0x00000100     # CONSENSUS-CRITICAL - typically 0x00000100
      parent_chain: BTC           # or LTC for Scrypt coins
```

---

## Step 2: Add Go Implementation

Create `src/stratum/internal/coin/<symbol>.go`:

```go
package coin

// NEWCOIN implements the Coin interface for New Coin.
type NEWCOIN struct {
    baseCoin
}

func init() {
    Register("NEWCOIN", func() Coin {
        return &NEWCOIN{
            baseCoin: baseCoin{
                symbol:    "NEWCOIN",
                algorithm: AlgoSHA256d, // or AlgoScrypt
            },
        }
    })
}

// ChainID returns the AuxPoW chain identifier.
// CONSENSUS-CRITICAL: Must match manifest value.
func (c *NEWCOIN) ChainID() uint32 {
    return 1234  // Must match manifest chain_id
}

// VersionBit returns the AuxPoW version bit.
// CONSENSUS-CRITICAL: Must match manifest value.
func (c *NEWCOIN) VersionBit() uint32 {
    return 0x00000100
}

// GenesisHash returns the genesis block hash.
// Used for validation at startup.
func (c *NEWCOIN) GenesisHash() string {
    return "000000..."  // Must match manifest genesis_hash
}
```

### Standalone Coins (No AuxPoW)

For coins without merge mining support, omit `ChainID()` and `VersionBit()`:

```go
package coin

type NEWCOIN struct {
    baseCoin
}

func init() {
    Register("NEWCOIN", func() Coin {
        return &NEWCOIN{
            baseCoin: baseCoin{
                symbol:    "NEWCOIN",
                algorithm: AlgoSHA256d,
            },
        }
    })
}

func (c *NEWCOIN) GenesisHash() string {
    return "000000..."
}
```

---

## Step 3: Validate

Validate manually:

1. Verify all required fields are present in the manifest entry
2. Verify ports are unique across all coins (`grep -n "port:" config/coins.manifest.yaml`)
3. Verify Go implementation exists: `ls src/stratum/internal/coin/<symbol>.go`
4. Build and run tests:

```bash
go test ./internal/coin/...
```
- Consensus values (chain_id, genesis_hash) match between manifest and Go code

---

## Step 4: Update Documentation

Add the new coin to:

1. **README.md** - Supported coins table and coin details
2. **docs/setup/OPERATIONS.md** - Storage requirements table
3. **docs/reference/REFERENCE.md** - Stratum ports table and services list
4. **install-windows.ps1** - ValidateSet and help text (if Windows support needed)

---

## Port Allocation

Stratum ports follow this pattern:

| Range | Algorithm | Coins |
|-------|-----------|-------|
| 3333-3335 | SHA-256d | DGB |
| 3336-3338 | Scrypt | DGB-SCRYPT |
| 4333-6335 | SHA-256d | BTC, BCH, BC2 |
| 7333-7335 | Scrypt | LTC |
| 8335-8342 | Scrypt | DOGE |
| 10335-12337 | Scrypt | PEP, CAT |
| 14335-18337 | SHA-256d | NMC, SYS, XMY, FBTC |
| 20335-20337 | SHA-256d | QBX |

When adding a new coin, select the next available port in the appropriate range.

Each coin uses 3 ports (typically consecutive):
- `N` - Stratum V1 (standard mining)
- `N+1` - Stratum V2 (binary protocol)
- `N+2` - Stratum TLS (encrypted)

> **Note:** DOGE uses non-consecutive ports (8335/8337/8342) due to historical allocation. New coins should use consecutive ports.

---

## Automated Coin Addition

For supported coins, use the automated script:

```bash
# Linux
./scripts/spiralpool-add-coin -s NEWCOIN -g https://github.com/newcoin/newcoin

# Windows
scripts\windows\spiralpool-add-coin.bat -s NEWCOIN -g https://github.com/newcoin/newcoin
```

The script:
1. Clones the coin repository
2. Parses `chainparams.cpp` for network parameters
3. Detects algorithm (SHA-256d or Scrypt)
4. Extracts AuxPoW settings if present
5. Fetches metadata from CoinGecko (if listed)
6. Generates manifest entry and Go implementation
7. Runs validation

---

## Common Issues

### Port Conflict

```
Error: Stratum port 8335 already in use by DOGE
```

Solution: Choose an unused port. Check `config/coins.manifest.yaml` for allocated ports.

### Genesis Hash Mismatch

```
Error: NEWCOIN genesis hash mismatch
  Manifest: 000000abc...
  Go code:  000000def...
```

Solution: Verify the correct genesis hash using `getblockhash 0` RPC call on a synced node.

### Chain ID Collision

```
Error: Chain ID 98 already used by DOGE
```

Solution: Each AuxPoW coin must have a unique chain ID. Use the official chain ID from the coin's source code.

### Missing SegWit Support

If the coin doesn't support SegWit:
- Set `supports_segwit: false` in manifest
- Set `bech32_hrp: null` if no native SegWit addresses

---

## Merge Mining Relationships

### BTC Parent Chain (SHA-256d)
```
BTC ──┬── NMC (Namecoin)
      ├── SYS (Syscoin)
      ├── XMY (Myriad)
      └── FBTC (Fractal Bitcoin)
```

### LTC Parent Chain (Scrypt)
```
LTC ──┬── DOGE (Dogecoin)
      └── PEP (PepeCoin)
```

### Standalone SHA-256d (Not Merge-Mineable)
```
BC2 (Bitcoin II)
BCH (Bitcoin Cash)
DGB (DigiByte)
QBX (Q-BitX)
```

---

## Checklist

- [ ] Gathered all required chain parameters
- [ ] Added manifest entry to `config/coins.manifest.yaml`
- [ ] Created Go implementation in `src/stratum/internal/coin/<symbol>.go`
- [ ] Ran `go test ./internal/coin/...` successfully
- [ ] Updated README.md with coin entry
- [ ] Updated docs/setup/OPERATIONS.md with coin entry
- [ ] Updated docs/reference/REFERENCE.md with ports and services
- [ ] Updated install-windows.ps1 (if Windows support needed)
- [ ] Added `install_{coin}()` function to `install.sh` with correct `{COIN}_BIN_DIR`
- [ ] Added verification check in `install.sh` matching the `{COIN}_BIN_DIR` path
- [ ] Added CLI path to `wait-for-node.sh` `get_cli_path()` (use `{coin}-bin/bin/` for coins with separate binary dirs)
- [ ] Tested node sync and stratum connection

---

*Spiral Pool — Convergent Spiral 1.2.3 — Built on what came before. Growing toward phi.*
