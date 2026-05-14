# Spiral Pool — Stratum TLS Certificates

Place your TLS certificate and private key here before starting the pool with TLS enabled.

Expected filenames:
  stratum.crt  — PEM-encoded TLS certificate (chain optional)
  stratum.key  — PEM-encoded private key (unencrypted)

## Self-signed (development / testing)

Generate a self-signed cert valid for 10 years:

    openssl req -x509 -newkey rsa:4096 -keyout stratum.key -out stratum.crt \
        -days 3650 -nodes -subj "/CN=spiral-stratum"

Miners connecting via TLS will need to trust this cert or disable cert verification
in their firmware (most ASIC firmware allows this).

## Let's Encrypt / ACME

Copy your fullchain.pem → stratum.crt and privkey.pem → stratum.key.
Renew and restart the stratum container when the cert expires.

## Without TLS

Leave this directory empty. The stratum container starts without TLS; miners
connect via plain stratum+tcp:// (V1) or stratum+sv2:// (V2 Noise, no certs needed).

The `TLS_CERT_FILE` and `TLS_KEY_FILE` env vars in docker-compose.yml point here.
If the files are absent the stratum skips TLS listener startup.
