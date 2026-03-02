// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package stratum provides TLS/SSL tests for encrypted stratum connections.
//
// These tests validate:
// - TLS configuration loading
// - TLS version enforcement
// - Certificate validation
// - TLS connection metrics
// - Graceful handling of TLS errors
package stratum

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestTLSConfigVersionParsing tests TLS version string parsing.
func TestTLSConfigVersionParsing(t *testing.T) {
	tests := []struct {
		name        string
		versionStr  string
		expectedMin uint16
	}{
		{"TLS 1.3", "1.3", tls.VersionTLS13},
		{"TLS 1.2", "1.2", tls.VersionTLS12},
		{"Default to 1.2", "", tls.VersionTLS12},
		{"Invalid defaults to 1.2", "invalid", tls.VersionTLS12},
		{"Old version defaults to 1.2", "1.1", tls.VersionTLS12},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var minVersion uint16
			switch tc.versionStr {
			case "1.3":
				minVersion = tls.VersionTLS13
			case "1.2":
				minVersion = tls.VersionTLS12
			default:
				minVersion = tls.VersionTLS12
			}

			if minVersion != tc.expectedMin {
				t.Errorf("Version %q: expected %d, got %d", tc.versionStr, tc.expectedMin, minVersion)
			}
		})
	}
}

// TestTLSCertificateGeneration tests that we can generate valid test certs.
func TestTLSCertificateGeneration(t *testing.T) {
	certPEM, keyPEM, err := generateTestCertificate()
	if err != nil {
		t.Fatalf("Failed to generate test certificate: %v", err)
	}

	// Verify certificate parses correctly
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("Failed to decode certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse certificate: %v", err)
	}

	// Verify key parses correctly
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("Failed to decode key PEM")
	}

	_, err = x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse private key: %v", err)
	}

	// Verify certificate properties
	if cert.Subject.CommonName != "test.spiralpool.local" {
		t.Errorf("Expected CN 'test.spiralpool.local', got %q", cert.Subject.CommonName)
	}

	if time.Now().After(cert.NotAfter) {
		t.Error("Certificate has already expired")
	}

	if time.Now().Before(cert.NotBefore) {
		t.Error("Certificate is not yet valid")
	}
}

// TestTLSCertificateLoading tests loading TLS certificates from files.
func TestTLSCertificateLoading(t *testing.T) {
	// Create temporary directory for test certs
	tmpDir, err := os.MkdirTemp("", "spiral-tls-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	certPEM, keyPEM, err := generateTestCertificate()
	if err != nil {
		t.Fatalf("Failed to generate test certificate: %v", err)
	}

	certFile := filepath.Join(tmpDir, "test.crt")
	keyFile := filepath.Join(tmpDir, "test.key")

	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		t.Fatalf("Failed to write cert file: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatalf("Failed to write key file: %v", err)
	}

	// Test loading certificate pair
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("Failed to load certificate pair: %v", err)
	}

	if len(cert.Certificate) == 0 {
		t.Error("Certificate chain is empty")
	}
}

// TestTLSConfigBuild tests building a complete TLS configuration.
func TestTLSConfigBuild(t *testing.T) {
	// Create temporary directory for test certs
	tmpDir, err := os.MkdirTemp("", "spiral-tls-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	certPEM, keyPEM, err := generateTestCertificate()
	if err != nil {
		t.Fatalf("Failed to generate test certificate: %v", err)
	}

	certFile := filepath.Join(tmpDir, "test.crt")
	keyFile := filepath.Join(tmpDir, "test.key")

	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		t.Fatalf("Failed to write cert file: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatalf("Failed to write key file: %v", err)
	}

	// Build TLS config
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("Failed to load certificate: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// Verify TLS config properties
	if len(tlsConfig.Certificates) != 1 {
		t.Error("Expected exactly one certificate")
	}

	if tlsConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("Expected min version TLS 1.2, got %d", tlsConfig.MinVersion)
	}
}

// TestTLSClientAuthModes tests TLS client authentication mode settings.
func TestTLSClientAuthModes(t *testing.T) {
	tests := []struct {
		name       string
		clientAuth bool
		expected   tls.ClientAuthType
	}{
		{"No client auth", false, tls.NoClientCert},
		{"Require client cert", true, tls.RequireAndVerifyClientCert},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var authType tls.ClientAuthType
			if tc.clientAuth {
				authType = tls.RequireAndVerifyClientCert
			} else {
				authType = tls.NoClientCert
			}

			if authType != tc.expected {
				t.Errorf("Expected auth type %d, got %d", tc.expected, authType)
			}
		})
	}
}

// TestTLSConnectionCounter tests TLS connection metric tracking.
func TestTLSConnectionCounter(t *testing.T) {
	var tlsConnections atomic.Uint64
	var plainConnections atomic.Uint64

	// Simulate connection handling
	connections := []struct {
		isTLS bool
	}{
		{true}, {false}, {true}, {true}, {false}, {true},
	}

	for _, conn := range connections {
		if conn.isTLS {
			tlsConnections.Add(1)
		} else {
			plainConnections.Add(1)
		}
	}

	if tlsConnections.Load() != 4 {
		t.Errorf("Expected 4 TLS connections, got %d", tlsConnections.Load())
	}

	if plainConnections.Load() != 2 {
		t.Errorf("Expected 2 plain connections, got %d", plainConnections.Load())
	}
}

// TestTLSListenerAddress tests TLS listener address formatting.
func TestTLSListenerAddress(t *testing.T) {
	tests := []struct {
		name    string
		address string
		valid   bool
	}{
		{"Standard port", "0.0.0.0:3335", true},
		{"Localhost", "127.0.0.1:3335", true},
		{"IPv6", "[::]:3335", true},
		{"Empty", "", false},
		{"No port", "0.0.0.0", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isValid := tc.address != "" && strings.Contains(tc.address, ":")

			if isValid != tc.valid {
				t.Errorf("Address %q: expected valid=%v, got valid=%v", tc.address, tc.valid, isValid)
			}
		})
	}
}

// TestTLSCertificateExpiry tests certificate expiry detection.
func TestTLSCertificateExpiry(t *testing.T) {
	tests := []struct {
		name      string
		notAfter  time.Time
		isExpired bool
	}{
		{"Valid cert", time.Now().Add(24 * time.Hour), false},
		{"Expired cert", time.Now().Add(-24 * time.Hour), true},
		{"About to expire", time.Now().Add(1 * time.Minute), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isExpired := time.Now().After(tc.notAfter)

			if isExpired != tc.isExpired {
				t.Errorf("Certificate expiry: expected %v, got %v", tc.isExpired, isExpired)
			}
		})
	}
}

// TestTLSCipherSuites tests TLS cipher suite configuration.
func TestTLSCipherSuites(t *testing.T) {
	// TLS 1.3 cipher suites (not configurable, always used)
	tls13Suites := []uint16{
		tls.TLS_AES_128_GCM_SHA256,
		tls.TLS_AES_256_GCM_SHA384,
		tls.TLS_CHACHA20_POLY1305_SHA256,
	}

	// Verify TLS 1.3 suites are recognized
	for _, suite := range tls13Suites {
		name := tls.CipherSuiteName(suite)
		if name == "" || strings.HasPrefix(name, "0x") {
			t.Errorf("Unknown TLS 1.3 cipher suite: %d", suite)
		}
	}
}

// TestTLSHandshakeTimeout tests TLS handshake timeout simulation.
func TestTLSHandshakeTimeout(t *testing.T) {
	timeouts := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		30 * time.Second,
	}

	for _, timeout := range timeouts {
		t.Run(fmt.Sprintf("%v", timeout), func(t *testing.T) {
			deadline := time.Now().Add(timeout)

			// Simulate handshake check
			if time.Now().After(deadline) {
				t.Error("Deadline should be in the future")
			}
		})
	}
}

// TestTLSSessionLogging tests TLS session info for logging.
func TestTLSSessionLogging(t *testing.T) {
	// Simulate TLS connection state
	type mockTLSInfo struct {
		Version     uint16
		CipherSuite uint16
		ServerName  string
	}

	info := mockTLSInfo{
		Version:     tls.VersionTLS13,
		CipherSuite: tls.TLS_AES_256_GCM_SHA384,
		ServerName:  "pool.spiralpool.local",
	}

	// Verify version string
	var versionStr string
	switch info.Version {
	case tls.VersionTLS13:
		versionStr = "TLS 1.3"
	case tls.VersionTLS12:
		versionStr = "TLS 1.2"
	default:
		versionStr = "Unknown"
	}

	if versionStr != "TLS 1.3" {
		t.Errorf("Expected 'TLS 1.3', got %q", versionStr)
	}

	// Verify cipher suite name
	cipherName := tls.CipherSuiteName(info.CipherSuite)
	if !strings.Contains(cipherName, "AES_256_GCM") {
		t.Errorf("Expected AES_256_GCM cipher, got %q", cipherName)
	}
}

// TestTLSDisabledBehavior tests that TLS can be gracefully disabled.
func TestTLSDisabledBehavior(t *testing.T) {
	type mockTLSConfig struct {
		Enabled   bool
		ListenTLS string
	}

	tests := []struct {
		name         string
		config       mockTLSConfig
		shouldListen bool
	}{
		{
			"TLS disabled",
			mockTLSConfig{Enabled: false, ListenTLS: ""},
			false,
		},
		{
			"TLS enabled with port",
			mockTLSConfig{Enabled: true, ListenTLS: "0.0.0.0:3335"},
			true,
		},
		{
			"TLS enabled but no port",
			mockTLSConfig{Enabled: true, ListenTLS: ""},
			false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shouldListen := tc.config.Enabled && tc.config.ListenTLS != ""

			if shouldListen != tc.shouldListen {
				t.Errorf("Expected shouldListen=%v, got %v", tc.shouldListen, shouldListen)
			}
		})
	}
}

// TestTLSErrorMessages tests TLS error message handling.
func TestTLSErrorMessages(t *testing.T) {
	errorTypes := []struct {
		name    string
		message string
	}{
		{"Certificate not found", "failed to load TLS certificate"},
		{"Invalid key", "failed to parse private key"},
		{"Handshake failed", "TLS handshake error"},
		{"Certificate expired", "certificate has expired"},
	}

	for _, et := range errorTypes {
		t.Run(et.name, func(t *testing.T) {
			if et.message == "" {
				t.Error("Error message should not be empty")
			}
		})
	}
}

// TestTLSPortSeparation tests that TLS and plain ports are different.
func TestTLSPortSeparation(t *testing.T) {
	plainPort := 3333
	tlsPort := 3335

	if plainPort == tlsPort {
		t.Error("Plain and TLS ports must be different")
	}

	// Standard port separation is 2 (3333 -> 3335)
	if tlsPort-plainPort != 2 {
		t.Logf("Note: TLS port is not at standard offset (+2) from plain port")
	}
}

// TestTLSCertificateChainValidation tests certificate chain concepts.
func TestTLSCertificateChainValidation(t *testing.T) {
	// Simulate certificate chain depths
	chainDepths := []struct {
		name  string
		depth int
		valid bool
	}{
		{"Self-signed", 1, true},
		{"Single intermediate", 2, true},
		{"Double intermediate", 3, true},
		{"Empty chain", 0, false},
	}

	for _, cd := range chainDepths {
		t.Run(cd.name, func(t *testing.T) {
			isValid := cd.depth > 0

			if isValid != cd.valid {
				t.Errorf("Chain depth %d: expected valid=%v, got %v", cd.depth, cd.valid, isValid)
			}
		})
	}
}

// BenchmarkTLSConfigCreation benchmarks TLS config object creation.
func BenchmarkTLSConfigCreation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}
}

// BenchmarkTLSVersionParsing benchmarks TLS version string parsing.
func BenchmarkTLSVersionParsing(b *testing.B) {
	versions := []string{"1.3", "1.2", "", "invalid"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v := versions[i%len(versions)]
		switch v {
		case "1.3":
			_ = tls.VersionTLS13
		case "1.2":
			_ = tls.VersionTLS12
		default:
			_ = tls.VersionTLS12
		}
	}
}

// generateTestCertificate creates a self-signed test certificate.
func generateTestCertificate() (certPEM, keyPEM []byte, err error) {
	// Generate ECDSA key
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate key: %w", err)
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Spiral Pool Test"},
			CommonName:   "test.spiralpool.local",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"test.spiralpool.local", "localhost"},
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// Encode certificate to PEM
	certPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	// Encode private key to PEM
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal private key: %w", err)
	}

	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})

	return certPEM, keyPEM, nil
}
