// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package stratum

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// writeTestCertPair writes a fresh self-signed cert/key pair and returns the paths.
func writeTestCertPair(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	certPEM, keyPEM, err := generateTestCertificate()
	if err != nil {
		t.Fatalf("generate test certificate: %v", err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "test.crt")
	keyFile = filepath.Join(dir, "test.key")
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

// freePort reserves and releases a port so the test can bind it deterministically.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

// TestListenTLSDrivesTLSListener proves the mechanism the multi-coin port depends
// on: a TLS listener is opened only when TLS.ListenTLS is populated. multi_port
// carried a tls_port that never reached this field, so the certificate loaded and
// nothing ever listened. With ListenTLS set, a TLS client completes a handshake.
func TestListenTLSDrivesTLSListener(t *testing.T) {
	certFile, keyFile := writeTestCertPair(t)
	plainPort, tlsPort := freePort(t), freePort(t)

	for _, tc := range []struct {
		name      string
		listenTLS string
		wantTLS   bool
	}{
		{"ListenTLS set — listener accepts TLS", fmt.Sprintf("127.0.0.1:%d", tlsPort), true},
		{"ListenTLS empty — nothing listens", "", false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.StratumConfig{
				Listen: fmt.Sprintf("127.0.0.1:%d", freePort(t)),
				TLS: config.TLSConfig{
					Enabled:    true,
					ListenTLS:  tc.listenTLS,
					CertFile:   certFile,
					KeyFile:    keyFile,
					MinVersion: "1.2",
				},
			}
			cfg.RateLimiting.PreAuthMessageLimit = 20
			cfg.Connection.MaxConnections = 16 // 0 would reject every connection on accept

			srv := NewServer(cfg, zap.NewNop())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := srv.Start(ctx); err != nil {
				t.Fatalf("server start: %v", err)
			}
			defer func() { _ = srv.Stop() }()

			addr := fmt.Sprintf("127.0.0.1:%d", tlsPort)
			conn, err := tls.DialWithDialer(
				&net.Dialer{Timeout: 3 * time.Second},
				"tcp", addr,
				&tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- self-signed test cert
			)
			if tc.wantTLS {
				if err != nil {
					t.Fatalf("expected a working TLS listener on %s, got: %v", addr, err)
				}
				_ = conn.Close()
			} else {
				if err == nil {
					_ = conn.Close()
					t.Fatalf("expected nothing listening on %s when ListenTLS is empty", addr)
				}
			}
			_ = plainPort
		})
	}
}
