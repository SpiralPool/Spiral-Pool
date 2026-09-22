// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"strings"
	"testing"

	"github.com/spiralpool/stratum/internal/config"
)

func tlsCoin(cert, key string) []config.CoinPoolConfig {
	return []config.CoinPoolConfig{{
		Symbol:  "DGB",
		Enabled: true,
		Stratum: config.CoinStratumConfig{
			Port:    3333,
			PortTLS: 3335,
			TLS: config.CoinTLSConfig{
				CertFile:   cert,
				KeyFile:    key,
				MinVersion: "1.3",
			},
		},
	}}
}

// TestMultiPortTLSPortOpensListener is the regression test for multi_port.tls_port
// having been inert: the value was carried into the scheduler but ListenTLS was
// never set, so the stratum server loaded the certificate and then opened no TLS
// listener at all. Without ListenTLS populated, nothing listens on tls_port.
func TestMultiPortTLSPortOpensListener(t *testing.T) {
	t.Parallel()

	cfg, err := buildMultiPortStratumConfig(
		tlsCoin("/etc/ssl/pool.crt", "/etc/ssl/pool.key"),
		config.MultiPortConfig{Enabled: true, Port: 16180, TLSPort: 16181},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != "0.0.0.0:16180" {
		t.Errorf("plaintext listen = %q, want 0.0.0.0:16180", cfg.Listen)
	}
	if !cfg.TLS.Enabled {
		t.Error("TLS.Enabled is false, so no TLS listener is created")
	}
	if cfg.TLS.ListenTLS != "0.0.0.0:16181" {
		t.Errorf("TLS.ListenTLS = %q, want 0.0.0.0:16181 — empty means the server opens no TLS listener", cfg.TLS.ListenTLS)
	}
	if cfg.TLS.CertFile != "/etc/ssl/pool.crt" || cfg.TLS.KeyFile != "/etc/ssl/pool.key" {
		t.Errorf("certificate material not inherited: %+v", cfg.TLS)
	}
	if cfg.TLS.MinVersion != "1.3" {
		t.Errorf("MinVersion = %q, want 1.3", cfg.TLS.MinVersion)
	}
}

// TestMultiPortNoTLSPortStaysPlaintext verifies the opt-in stays opt-in: with no
// tls_port the multi port must not advertise a TLS listener even though the coin
// it inherits from has its own TLS port configured.
func TestMultiPortNoTLSPortStaysPlaintext(t *testing.T) {
	t.Parallel()

	cfg, err := buildMultiPortStratumConfig(
		tlsCoin("/etc/ssl/pool.crt", "/etc/ssl/pool.key"),
		config.MultiPortConfig{Enabled: true, Port: 16180},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TLS.Enabled || cfg.TLS.ListenTLS != "" {
		t.Errorf("multi port should stay plaintext without tls_port, got Enabled=%v ListenTLS=%q",
			cfg.TLS.Enabled, cfg.TLS.ListenTLS)
	}
}

// TestMultiPortTLSPortWithoutCertificateFails checks the operator is told, rather
// than silently served plaintext, when tls_port is set with nothing to serve it with.
func TestMultiPortTLSPortWithoutCertificateFails(t *testing.T) {
	t.Parallel()

	_, err := buildMultiPortStratumConfig(
		tlsCoin("", ""),
		config.MultiPortConfig{Enabled: true, Port: 16180, TLSPort: 16181},
	)
	if err == nil {
		t.Fatal("expected an error when tls_port is set but no certificate is available")
	}
	if !strings.Contains(err.Error(), "tls_port") {
		t.Errorf("error should name multi_port.tls_port, got: %v", err)
	}
}

// TestMultiPortNoEnabledCoin keeps the pre-existing failure mode intact.
func TestMultiPortNoEnabledCoin(t *testing.T) {
	t.Parallel()

	_, err := buildMultiPortStratumConfig(
		[]config.CoinPoolConfig{{Symbol: "DGB", Enabled: false}},
		config.MultiPortConfig{Enabled: true, Port: 16180},
	)
	if err == nil || !strings.Contains(err.Error(), "no enabled coin") {
		t.Fatalf("expected 'no enabled coin' error, got: %v", err)
	}
}
