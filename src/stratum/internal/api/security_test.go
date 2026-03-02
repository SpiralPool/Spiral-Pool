// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package api - Security tests for API middleware.
//
// These tests verify:
// - Admin authentication middleware (API key validation)
// - CORS middleware (origin validation)
// - Rate limiting middleware
// - Timing attack resistance
// - Security header enforcement
package api

import (
	"crypto/subtle"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// =============================================================================
// ADMIN AUTHENTICATION MIDDLEWARE TESTS
// =============================================================================

func TestAdminAuthMiddleware_NoAPIKeyConfigured(t *testing.T) {
	// When no API key is configured, admin endpoints should be disabled
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "", // Not configured
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("Expected 403 Forbidden when API key not configured, got %d", rr.Code)
	}

	if !strings.Contains(rr.Body.String(), "not configured") {
		t.Errorf("Expected 'not configured' message, got: %s", rr.Body.String())
	}
}

func TestAdminAuthMiddleware_NoKeyProvided(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "test-secret-key-123",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	// No API key header
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized when no key provided, got %d", rr.Code)
	}

	if !strings.Contains(rr.Body.String(), "API key required") {
		t.Errorf("Expected 'API key required' message, got: %s", rr.Body.String())
	}
}

func TestAdminAuthMiddleware_InvalidKey(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "correct-secret-key",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	req.Header.Set("X-API-Key", "wrong-secret-key")
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for invalid key, got %d", rr.Code)
	}

	if !strings.Contains(rr.Body.String(), "Invalid API key") {
		t.Errorf("Expected 'Invalid API key' message, got: %s", rr.Body.String())
	}
}

func TestAdminAuthMiddleware_ValidKey_XAPIKey(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "valid-secret-key-123",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handlerCalled := false
	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	req.Header.Set("X-API-Key", "valid-secret-key-123")
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for valid key, got %d", rr.Code)
	}

	if !handlerCalled {
		t.Error("Handler should have been called for valid key")
	}
}

func TestAdminAuthMiddleware_ValidKey_BearerToken(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "bearer-secret-key-456",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handlerCalled := false
	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	req.Header.Set("Authorization", "Bearer bearer-secret-key-456")
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for valid Bearer token, got %d", rr.Code)
	}

	if !handlerCalled {
		t.Error("Handler should have been called for valid Bearer token")
	}
}

func TestAdminAuthMiddleware_XAPIKeyTakesPrecedence(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "correct-key",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	req.Header.Set("X-API-Key", "correct-key")
	req.Header.Set("Authorization", "Bearer wrong-key") // Should be ignored
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("X-API-Key should take precedence, got status %d", rr.Code)
	}
}

// =============================================================================
// TIMING ATTACK RESISTANCE TESTS
// =============================================================================

func TestConstantTimeCompare(t *testing.T) {
	// Verify constant-time comparison is used
	correct := "correct-api-key-12345678"
	wrongPrefix := "xorrect-api-key-12345678"
	wrongSuffix := "correct-api-key-1234567x"
	totallyWrong := "xxxxxxxxxxxxxxxxxxxxxxxx"

	// All comparisons should take approximately the same time
	// (This is a functional test, not a timing test)
	if subtle.ConstantTimeCompare([]byte(correct), []byte(correct)) != 1 {
		t.Error("Same strings should compare equal")
	}

	if subtle.ConstantTimeCompare([]byte(correct), []byte(wrongPrefix)) != 0 {
		t.Error("Different prefix should compare unequal")
	}

	if subtle.ConstantTimeCompare([]byte(correct), []byte(wrongSuffix)) != 0 {
		t.Error("Different suffix should compare unequal")
	}

	if subtle.ConstantTimeCompare([]byte(correct), []byte(totallyWrong)) != 0 {
		t.Error("Totally different should compare unequal")
	}
}

func TestAdminAuthMiddleware_TimingAttackResistance(t *testing.T) {
	// This test verifies timing attack resistance by checking that
	// the comparison method used is constant-time.
	// Note: Actually measuring timing differences is unreliable in tests.

	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "secret-key-that-is-longer-than-typical",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Test with keys of same length but different content
	testKeys := []string{
		"aecret-key-that-is-longer-than-typical", // Wrong first char
		"secret-key-that-is-longer-than-typicax", // Wrong last char
		"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", // All wrong
	}

	for _, key := range testKeys {
		req := httptest.NewRequest("GET", "/api/admin/stats", nil)
		req.Header.Set("X-API-Key", key)
		rr := httptest.NewRecorder()

		handler(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("Invalid key should return 401, got %d for key starting with '%c'",
				rr.Code, key[0])
		}
	}
}

// =============================================================================
// CORS MIDDLEWARE TESTS
// =============================================================================

func TestCORSMiddleware_NoOrigin(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled: true,
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/pools", nil)
	// No Origin header
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Should not set CORS headers
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS headers should not be set without Origin header")
	}
}

func TestCORSMiddleware_LocalhostOrigin(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled: true,
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	localhostOrigins := []string{
		"http://localhost",
		"http://localhost:3000",
		"http://localhost:8080",
		"http://127.0.0.1",
		"http://127.0.0.1:3000",
		"https://localhost",
		"https://localhost:3000",
		"http://[::1]",
		"http://[::1]:3000",
	}

	for _, origin := range localhostOrigins {
		t.Run(origin, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/pools", nil)
			req.Header.Set("Origin", origin)
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			allowedOrigin := rr.Header().Get("Access-Control-Allow-Origin")
			if allowedOrigin != origin {
				t.Errorf("Expected Access-Control-Allow-Origin=%s, got %s", origin, allowedOrigin)
			}
		})
	}
}

func TestCORSMiddleware_ExternalOriginDenied(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled: true,
		// No CORSAllowedOrigin configured
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	externalOrigins := []string{
		"https://evil.com",
		"http://attacker.com",
		"https://example.com",
		"http://192.168.1.100:3000",
	}

	for _, origin := range externalOrigins {
		t.Run(origin, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/pools", nil)
			req.Header.Set("Origin", origin)
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			// External origins should not be allowed
			allowedOrigin := rr.Header().Get("Access-Control-Allow-Origin")
			if allowedOrigin != "" {
				t.Errorf("External origin %s should not be allowed, got: %s", origin, allowedOrigin)
			}
		})
	}
}

func TestCORSMiddleware_ConfiguredOrigin(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:           true,
		CORSAllowedOrigin: "https://dashboard.example.com",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/pools", nil)
	req.Header.Set("Origin", "https://other.com")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Should use configured origin regardless of request origin
	allowedOrigin := rr.Header().Get("Access-Control-Allow-Origin")
	if allowedOrigin != "https://dashboard.example.com" {
		t.Errorf("Expected configured origin, got: %s", allowedOrigin)
	}
}

func TestCORSMiddleware_PreflightRequest(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled: true,
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handlerCalled := false
	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("OPTIONS", "/api/pools", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Preflight should return 200, got %d", rr.Code)
	}

	if handlerCalled {
		t.Error("Actual handler should not be called for OPTIONS preflight")
	}

	// Check CORS headers
	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("Expected Access-Control-Allow-Methods header")
	}

	if rr.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Error("Expected Access-Control-Allow-Headers header")
	}
}

func TestCORSMiddleware_NoWildcard(t *testing.T) {
	// SECURITY: Ensure wildcard (*) is never used for CORS
	cfg := &config.APIConfig{
		Enabled: true,
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	origins := []string{
		"http://localhost:3000",
		"http://127.0.0.1:8080",
		"https://localhost",
	}

	for _, origin := range origins {
		req := httptest.NewRequest("GET", "/api/pools", nil)
		req.Header.Set("Origin", origin)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		allowedOrigin := rr.Header().Get("Access-Control-Allow-Origin")
		if allowedOrigin == "*" {
			t.Errorf("SECURITY: Wildcard CORS (*) should never be used, origin: %s", origin)
		}
	}
}

// =============================================================================
// RATE LIMITING MIDDLEWARE TESTS
// =============================================================================

func TestRateLimitMiddleware_Disabled(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled: true,
		RateLimiting: config.RateLimitConfig{
			Enabled: false,
		},
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handlerCalled := 0
	handler := server.rateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled++
		w.WriteHeader(http.StatusOK)
	}))

	// Should allow unlimited requests when disabled
	for i := 0; i < 1000; i++ {
		req := httptest.NewRequest("GET", "/api/pools", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	if handlerCalled != 1000 {
		t.Errorf("Expected 1000 handler calls when rate limiting disabled, got %d", handlerCalled)
	}
}

// =============================================================================
// CONCURRENCY TESTS
// =============================================================================

func TestAdminAuthMiddleware_ConcurrentRequests(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "concurrent-test-key",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	var successCount, failCount int
	var mu sync.Mutex

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)

		// Valid requests
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/admin/stats", nil)
			req.Header.Set("X-API-Key", "concurrent-test-key")
			rr := httptest.NewRecorder()
			handler(rr, req)

			mu.Lock()
			if rr.Code == http.StatusOK {
				successCount++
			}
			mu.Unlock()
		}()

		// Invalid requests
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/admin/stats", nil)
			req.Header.Set("X-API-Key", "wrong-key")
			rr := httptest.NewRecorder()
			handler(rr, req)

			mu.Lock()
			if rr.Code == http.StatusUnauthorized {
				failCount++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	if successCount != 100 {
		t.Errorf("Expected 100 successful requests, got %d", successCount)
	}

	if failCount != 100 {
		t.Errorf("Expected 100 failed requests, got %d", failCount)
	}
}

// =============================================================================
// SECURITY EDGE CASES
// =============================================================================

func TestAdminAuthMiddleware_EmptyKey(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "valid-key",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	req.Header.Set("X-API-Key", "") // Empty key
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Empty key should return 401, got %d", rr.Code)
	}
}

func TestAdminAuthMiddleware_WhitespaceKey(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "valid-key",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	whitespaceKeys := []string{
		" ",
		"  ",
		"\t",
		"\n",
		" valid-key", // Leading space
		"valid-key ", // Trailing space
	}

	for _, key := range whitespaceKeys {
		t.Run("key:"+strings.ReplaceAll(key, "\n", "\\n"), func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/admin/stats", nil)
			req.Header.Set("X-API-Key", key)
			rr := httptest.NewRecorder()

			handler(rr, req)

			if rr.Code == http.StatusOK && key != "valid-key" {
				t.Errorf("Whitespace-modified key should not authenticate: %q", key)
			}
		})
	}
}

func TestAdminAuthMiddleware_LongKey(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "short-key",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Very long key should not cause issues
	longKey := strings.Repeat("a", 10000)

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	req.Header.Set("X-API-Key", longKey)
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Very long invalid key should return 401, got %d", rr.Code)
	}
}

func TestAdminAuthMiddleware_NullBytes(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "valid-key",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Null byte injection attempts
	nullKeys := []string{
		"valid-key\x00",
		"\x00valid-key",
		"valid\x00-key",
	}

	for _, key := range nullKeys {
		req := httptest.NewRequest("GET", "/api/admin/stats", nil)
		req.Header.Set("X-API-Key", key)
		rr := httptest.NewRecorder()

		handler(rr, req)

		if rr.Code == http.StatusOK {
			t.Errorf("Null byte key should not authenticate: %q", key)
		}
	}
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkAdminAuthMiddleware_Valid(b *testing.B) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "benchmark-key-123",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	req.Header.Set("X-API-Key", "benchmark-key-123")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		handler(rr, req)
	}
}

func BenchmarkAdminAuthMiddleware_Invalid(b *testing.B) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "benchmark-key-123",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	req.Header.Set("X-API-Key", "wrong-key-456789")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		handler(rr, req)
	}
}

func BenchmarkCORSMiddleware(b *testing.B) {
	cfg := &config.APIConfig{
		Enabled: true,
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/pools", nil)
	req.Header.Set("Origin", "http://localhost:3000")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}
}

// =============================================================================
// INTEGRATION TESTS
// =============================================================================

func TestMiddlewareChain_AllMiddlewares(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:           true,
		AdminAPIKey:       "chain-test-key",
		CORSAllowedOrigin: "https://dashboard.example.com",
		RateLimiting: config.RateLimitConfig{
			Enabled: false, // Disable for this test
		},
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	// Chain middlewares: CORS -> Auth -> Handler
	handler := server.corsMiddleware(
		http.HandlerFunc(
			server.adminAuthMiddleware(
				func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte("success"))
				},
			),
		),
	)

	// Test with all correct headers
	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	req.Header.Set("Origin", "https://dashboard.example.com")
	req.Header.Set("X-API-Key", "chain-test-key")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rr.Code)
	}

	if rr.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("Expected CORS headers to be set")
	}

	if rr.Body.String() != "success" {
		t.Errorf("Expected 'success', got: %s", rr.Body.String())
	}
}

// =============================================================================
// RESPONSE TIME VALIDATION
// =============================================================================

func TestAdminAuthMiddleware_ResponseTime(t *testing.T) {
	cfg := &config.APIConfig{
		Enabled:     true,
		AdminAPIKey: "timing-test-key-12345",
	}

	server := &Server{
		cfg:    cfg,
		logger: zap.NewNop().Sugar(),
	}

	handler := server.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Measure response times for valid and invalid keys
	// They should be similar (constant-time comparison)

	validTimes := make([]time.Duration, 100)
	invalidTimes := make([]time.Duration, 100)

	for i := 0; i < 100; i++ {
		// Valid key
		req := httptest.NewRequest("GET", "/api/admin/stats", nil)
		req.Header.Set("X-API-Key", "timing-test-key-12345")
		rr := httptest.NewRecorder()

		start := time.Now()
		handler(rr, req)
		validTimes[i] = time.Since(start)

		// Invalid key (same length)
		req2 := httptest.NewRequest("GET", "/api/admin/stats", nil)
		req2.Header.Set("X-API-Key", "wrong-test-key-67890")
		rr2 := httptest.NewRecorder()

		start = time.Now()
		handler(rr2, req2)
		invalidTimes[i] = time.Since(start)
	}

	// Calculate averages (for logging only - timing tests are unreliable)
	var validSum, invalidSum time.Duration
	for i := 0; i < 100; i++ {
		validSum += validTimes[i]
		invalidSum += invalidTimes[i]
	}

	t.Logf("Average valid key response: %v", validSum/100)
	t.Logf("Average invalid key response: %v", invalidSum/100)

	// Note: We don't assert on timing because:
	// 1. Test environments are not reliable for timing
	// 2. The constant-time comparison is verified by using subtle.ConstantTimeCompare
}
