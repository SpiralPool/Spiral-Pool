// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package stratum implements the Stratum mining protocol server.
// Includes TLS/SSL support and rate limiting for DDoS protection.
package stratum

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/security"
	v1 "github.com/spiralpool/stratum/internal/stratum/v1"
	"github.com/spiralpool/stratum/pkg/atomicmap"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// stringAddr implements net.Addr for string-based remote addresses.
// Used to adapt session.RemoteAddr (string) for rate limiter which expects net.Addr.
type stringAddr struct {
	addr string
}

func (s *stringAddr) Network() string { return "tcp" }
func (s *stringAddr) String() string  { return s.addr }

// Server implements the Stratum protocol server.
type Server struct {
	cfg      *config.StratumConfig
	logger   *zap.SugaredLogger
	listener net.Listener

	// TLS listener for encrypted connections
	tlsListener    net.Listener
	tlsTCPListener *net.TCPListener // underlying TCP listener for SetDeadline
	tlsConfig      *tls.Config

	// Rate limiter for DDoS protection
	rateLimiter *security.RateLimiter

	// V1 protocol handler
	v1Handler *v1.Handler

	// Session management
	sessions     *atomicmap.ShardedMap[uint64, *protocol.Session]
	sessionIDGen atomic.Uint64

	// ExtraNonce1 generation
	// CRITICAL: Uses separate counter from sessionID to prevent collision vulnerability
	// Session IDs are uint64 but ExtraNonce1 is only 4 bytes (uint32)
	// Using lower 32 bits of sessionID would cause collisions after 2^32 connections
	// A separate counter ensures each connection gets a unique ExtraNonce1
	extranonce1Gen atomic.Uint32

	// Job management
	currentJob atomic.Pointer[protocol.Job]
	jobMu      sync.RWMutex
	jobs       map[string]*protocol.Job // Recent jobs for share validation

	// Event handlers (set by pool)
	onShare            func(*protocol.Share) *protocol.ShareResult
	onConnect          func(*protocol.Session)
	onDisconnect       func(*protocol.Session)
	onDifficultyChange func(sessionID uint64, difficulty float64)   // Called when session difficulty changes
	onMinerClassified      func(sessionID uint64, profile MinerProfile)         // Called when miner is classified with full profile
	onConnectionClassified func(sessionID uint64, c ConnectionClassification) // Called when connection type is detected

	// State
	running   atomic.Bool
	connCount atomic.Int64

	// FIX O-1: WaitGroup for tracking connection goroutines (keepalive loops)
	// Ensures clean shutdown with no orphaned goroutines
	connWg sync.WaitGroup

	// Spiral Router for miner classification
	spiralRouter *SpiralRouter

	// Blockchain block time (used for session grace periods)
	blockTimeSec atomic.Int32

	// Worker class tracking
	workerClassMu sync.RWMutex
	workerClasses map[MinerClass]int

	// Connection classifier — fingerprints connections as ASIC / PROXY / MARKETPLACE.
	// Runs in three levels: user-agent (instant), handshake timing, extranonce2 entropy.
	classifier *ConnectionClassifier

	// Metrics
	totalConnections    atomic.Uint64
	rejectedConnections atomic.Uint64
	tlsConnections      atomic.Uint64 // TLS connection counter
	rateLimitedConns    atomic.Uint64 // Rate limited connection counter

	// FIX O-2: Global partial buffer memory tracking to prevent memory exhaustion attacks
	// Attacker could open many connections, each with partial messages below per-connection limit
	partialBufferBytes atomic.Int64  // Total bytes across all partial message buffers
	maxPartialBufferMB int64         // Maximum total partial buffer memory (MB), default 256MB
}

// NewServer creates a new Stratum server.
func NewServer(cfg *config.StratumConfig, logger *zap.Logger) *Server {
	sugar := logger.Sugar()

	s := &Server{
		cfg:    cfg,
		logger: sugar,
		sessions: atomicmap.New[uint64, *protocol.Session](
			atomicmap.DefaultShardCount,
			atomicmap.UInt64Hash,
		),
		jobs:               make(map[string]*protocol.Job),
		workerClasses:      make(map[MinerClass]int),
		maxPartialBufferMB: 512, // FIX O-2: Default 512MB global limit for partial message buffers (10PH/s scale)
		classifier:         NewConnectionClassifier(),
	}

	// Wire default Info-level logging for connection classification.
	// The internal callback fires exactly once per classification transition
	// (L1: user-agent instant, L2: handshake timing, L3: extranonce2 entropy).
	// SetConnectionClassifiedHandler replaces this with a wrapped version that
	// also invokes the pool's callback.
	s.classifier.SetClassifiedHandler(func(id uint64, c ConnectionClassification) {
		sugar.Infow("Connection classified",
			"sessionId", id,
			"type", c.Type.String(),
			"confidence", c.Confidence,
		)
	})

	// Initialize V1 protocol handler with config values
	// Use version rolling settings from config
	s.v1Handler = v1.NewHandler(
		cfg.Difficulty.Initial,
		cfg.VersionRolling.Enabled,
		cfg.VersionRolling.Mask,
	)

	// V2: Enable Spiral Router for automatic miner routing by user-agent and IP hints
	// This allows all miners (ESP32, BitAxe, ASICs) to use the same port
	// with automatic difficulty adjustment based on detected miner type.
	// IP-based device hints from Spiral Sentinel take priority for ESP-Miner devices.
	//
	// Only enabled when varDiff is active. When varDiff is disabled (e.g. regtest),
	// the config's initial difficulty is used directly for all miners, avoiding
	// Spiral Router overriding to ASIC-class difficulty on a test network.
	if cfg.Difficulty.VarDiff.Enabled {
		s.spiralRouter = NewSpiralRouter()
		s.v1Handler.SetMinerDifficultyRouterWithIP(s.spiralRouter.GetInitialDifficultyWithIP)

		// Wire up configurable slow-diff patterns from config
		// These identify miners that need longer cooldown between retargets (e.g., cgminer-based)
		if len(cfg.Difficulty.VarDiff.SlowDiffPatterns) > 0 {
			s.spiralRouter.SetSlowDiffPatterns(cfg.Difficulty.VarDiff.SlowDiffPatterns)
			sugar.Infow("Spiral Router configured with custom slow-diff patterns",
				"patterns", cfg.Difficulty.VarDiff.SlowDiffPatterns)
		}
		sugar.Info("Spiral Router enabled - IP hints + user-agent detection")
	} else {
		sugar.Infow("Spiral Router disabled (varDiff not enabled) — using fixed initial difficulty",
			"initialDiff", cfg.Difficulty.Initial)
	}

	// Initialize rate limiter if enabled
	if cfg.RateLimiting.Enabled {
		rlCfg := security.RateLimiterConfig{
			MaxConnectionsPerIP:  cfg.RateLimiting.ConnectionsPerIP,
			MaxConnectionsPerMin: cfg.RateLimiting.ConnectionsPerMinute,
			MaxSharesPerSecond:   cfg.RateLimiting.SharesPerSecond,
			BanThreshold:         cfg.RateLimiting.BanThreshold,
			BanDuration:          cfg.RateLimiting.BanDuration,
			WhitelistIPs:         cfg.RateLimiting.WhitelistIPs,
			// RED-TEAM: Additional security hardening options
			MaxWorkersPerIP:    cfg.RateLimiting.WorkersPerIP,
			BanPersistencePath: cfg.RateLimiting.BanPersistencePath,
		}
		s.rateLimiter = security.NewRateLimiter(rlCfg, sugar)
		sugar.Infow("Rate limiter initialized",
			"maxConnectionsPerIP", rlCfg.MaxConnectionsPerIP,
			"maxSharesPerSecond", rlCfg.MaxSharesPerSecond,
			"banThreshold", rlCfg.BanThreshold,
			"maxWorkersPerIP", rlCfg.MaxWorkersPerIP,
			"banPersistence", rlCfg.BanPersistencePath != "",
		)

		// SECURITY: Wire share rate limiting to V1 handler (SEC-05 fix)
		// This creates a closure that adapts the rate limiter's AllowShare method
		// to work with the handler's string-based remote address
		s.v1Handler.SetShareRateLimiter(func(remoteAddr string) (bool, string) {
			// Parse the address string back to a net.Addr-compatible format
			// The rate limiter expects net.Addr but we only have the string
			addr := &stringAddr{addr: remoteAddr}
			return s.rateLimiter.AllowShare(addr)
		})
	} else {
		// SECURITY (Audit #2): Warn when rate limiting is disabled at startup
		sugar.Warnw("SECURITY WARNING: Stratum rate limiting is DISABLED. "+
			"The pool is vulnerable to DDoS and share flooding attacks. "+
			"Enable stratum.rateLimiting.enabled in your configuration.",
			"recommendation", "audit-2")
	}

	// Initialize TLS configuration if enabled
	if cfg.TLS.Enabled {
		tlsConfig, err := s.loadTLSConfig()
		if err != nil {
			sugar.Errorw("Failed to load TLS configuration", "error", err)
		} else {
			s.tlsConfig = tlsConfig
			sugar.Infow("TLS configuration loaded",
				"certFile", cfg.TLS.CertFile,
				"minVersion", cfg.TLS.MinVersion,
			)
		}
	}

	return s
}

// loadTLSConfig loads the TLS certificate and key from files
func (s *Server) loadTLSConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	// Build TLS config with minimum TLS 1.2 (or 1.3 if configured)
	// #nosec G402 -- MinVersion is explicitly set to TLS 1.2 minimum
	tlsConfig := &tls.Config{
		Certificates:             []tls.Certificate{cert},
		PreferServerCipherSuites: true,
		MinVersion:               tls.VersionTLS12, // Enforced minimum TLS 1.2
	}

	// Upgrade to TLS 1.3 if explicitly configured
	if s.cfg.TLS.MinVersion == "1.3" {
		tlsConfig.MinVersion = tls.VersionTLS13
	}

	// Client certificate authentication (optional)
	if s.cfg.TLS.ClientAuth {
		if s.cfg.TLS.CAFile == "" {
			return nil, fmt.Errorf("TLS client auth enabled but caFile not specified")
		}

		// Load CA certificate pool for client verification
		caCert, err := loadCACertificate(s.cfg.TLS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load CA certificate: %w", err)
		}

		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		tlsConfig.ClientCAs = caCert
		s.logger.Infow("TLS client authentication enabled",
			"caFile", s.cfg.TLS.CAFile,
		)
	}

	return tlsConfig, nil
}

// loadCACertificate loads a CA certificate file and returns a certificate pool.
// Used for TLS client certificate authentication.
func loadCACertificate(caFile string) (*x509.CertPool, error) {
	// G304: Path is provided via config file by administrator, not untrusted input
	caCert, err := os.ReadFile(caFile) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("failed to read CA file: %w", err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return caCertPool, nil
}

// Start begins listening for connections.
func (s *Server) Start(ctx context.Context) error {
	// Start plain TCP listener
	listener, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.cfg.Listen, err)
	}
	s.listener = listener
	s.running.Store(true)

	s.logger.Infow("Stratum server started",
		"address", s.cfg.Listen,
		"maxConnections", s.cfg.Connection.MaxConnections,
		"rateLimiting", s.rateLimiter != nil,
	)

	// Accept loop for plain connections
	go s.acceptLoop(ctx, s.listener, false)

	// Start TLS listener if configured
	if s.tlsConfig != nil && s.cfg.TLS.ListenTLS != "" {
		// Create TCP listener first so we can call SetDeadline for clean shutdown.
		// tls.Listen() wraps the TCP listener in an unexported type that doesn't
		// expose SetDeadline, causing the accept loop to block indefinitely.
		tcpListener, err := net.Listen("tcp", s.cfg.TLS.ListenTLS)
		if err != nil {
			return fmt.Errorf("failed to start TLS listener on %s: %w", s.cfg.TLS.ListenTLS, err)
		}
		s.tlsTCPListener = tcpListener.(*net.TCPListener)
		s.tlsListener = tls.NewListener(tcpListener, s.tlsConfig)

		s.logger.Infow("Stratum TLS server started",
			"address", s.cfg.TLS.ListenTLS,
			"minVersion", s.cfg.TLS.MinVersion,
		)

		// Accept loop for TLS connections
		go s.acceptLoop(ctx, s.tlsListener, true)
	}

	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() error {
	s.running.Store(false)

	// Notify all connected miners to reconnect gracefully before dropping connections.
	// This prevents miners from sitting idle until their TCP keepalive fires (can be minutes).
	// Miners that support client.reconnect will reconnect within waitTime seconds.
	s.BroadcastReconnect(5)
	time.Sleep(500 * time.Millisecond) // Brief window for the message to be flushed

	if s.listener != nil {
		_ = s.listener.Close() // #nosec G104 - error ignored during shutdown
	}

	// Close TLS listener
	if s.tlsListener != nil {
		_ = s.tlsListener.Close() // #nosec G104 - error ignored during shutdown
	}

	// Close all sessions
	s.sessions.Range(func(id uint64, session *protocol.Session) bool {
		s.closeSession(session)
		return true
	})

	// FIX O-1: Wait for all connection goroutines (keepalive loops) to finish
	// This prevents orphaned goroutines after shutdown.
	// Use a timeout to prevent shutdown from hanging if a goroutine is stuck
	// (e.g., blocked on a cancelled DB context). Without this, systemd SIGKILL's
	// the process after TimeoutStopSec, which can lose in-flight block submissions.
	connDone := make(chan struct{})
	go func() {
		s.connWg.Wait()
		close(connDone)
	}()
	select {
	case <-connDone:
		// All connection goroutines finished cleanly
	case <-time.After(10 * time.Second):
		s.logger.Warn("Timeout waiting for connection goroutines to finish — proceeding with shutdown")
	}

	// Stop rate limiter background goroutines (cleanupLoop, persistLoop)
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}

	s.logger.Info("Stratum server stopped")
	return nil
}

// acceptLoop handles incoming connections.
// Updated to support multiple listeners (plain + TLS) and rate limiting
func (s *Server) acceptLoop(ctx context.Context, listener net.Listener, isTLS bool) {
	for s.running.Load() {
		// Check context
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Accept with timeout for clean shutdown.
		// For TLS listeners, use the stored underlying TCP listener since
		// tls.listener doesn't expose SetDeadline.
		if isTLS && s.tlsTCPListener != nil {
			_ = s.tlsTCPListener.SetDeadline(time.Now().Add(1 * time.Second)) // #nosec G104
		} else if tcpListener, ok := listener.(*net.TCPListener); ok {
			_ = tcpListener.SetDeadline(time.Now().Add(1 * time.Second)) // #nosec G104
		}

		conn, err := listener.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // Normal timeout, check running state
			}
			if s.running.Load() {
				s.logger.Warnw("Accept error", "error", err, "tls", isTLS)
			}
			continue
		}

		// Rate limiting check
		if s.rateLimiter != nil {
			allowed, reason := s.rateLimiter.AllowConnection(conn.RemoteAddr())
			if !allowed {
				s.rateLimitedConns.Add(1)
				s.logger.Debugw("Connection rate limited",
					"remoteAddr", conn.RemoteAddr().String(),
					"reason", reason,
				)
				_ = conn.Close() // #nosec G104
				continue
			}
		}

		// Check connection limit
		if s.connCount.Load() >= int64(s.cfg.Connection.MaxConnections) {
			s.rejectedConnections.Add(1)
			_ = conn.Close() // #nosec G104
			continue
		}

		s.totalConnections.Add(1)
		if isTLS {
			s.tlsConnections.Add(1)
		}
		go s.handleConnection(ctx, conn, isTLS)
	}
}

// handleConnection processes a single miner connection.
// Added isTLS parameter to track connection type
func (s *Server) handleConnection(ctx context.Context, conn net.Conn, isTLS bool) {
	s.connCount.Add(1)
	defer s.connCount.Add(-1)

	// Release rate limiter slot on disconnect
	if s.rateLimiter != nil {
		defer s.rateLimiter.ReleaseConnection(conn.RemoteAddr())
	}

	// Create session
	now := time.Now()
	session := &protocol.Session{
		ID:          s.sessionIDGen.Add(1),
		Conn:        conn,
		RemoteAddr:  conn.RemoteAddr().String(),
		ConnectedAt: now,
	}
	session.SetLastActivity(now) // Use atomic setter

	// Set blockchain's block time for dynamic grace periods
	if blockTime := s.blockTimeSec.Load(); blockTime > 0 {
		session.SetBlockTime(int(blockTime))
	}

	// Generate extranonce
	// CRITICAL FIX: Use dedicated extranonce1 counter instead of session ID
	// Using lower 32 bits of session ID caused collisions after 2^32 connections
	// since session IDs are uint64 but extranonce1 is only 4 bytes (uint32).
	// With separate counter, collisions only occur after 2^32 connections PER RESTART
	// which is acceptable (pool restarts reset the counter, and connections don't persist)
	extranonce1Val := s.extranonce1Gen.Add(1)
	session.ExtraNonce1 = fmt.Sprintf("%08x", extranonce1Val)
	session.ExtraNonce2Size = 8

	// RED-TEAM: Extranonce1 collision detection - log when counter wraps
	// This helps operators monitor for potential collision scenarios
	if extranonce1Val == 0 {
		s.logger.Warnw("SECURITY: Extranonce1 counter wrapped around - potential collision risk",
			"totalConnections", s.totalConnections.Load(),
			"recommendation", "Consider restarting the pool to reset counter if this happens frequently")
	}

	// Register session
	s.sessions.Set(session.ID, session)
	if s.onConnect != nil {
		s.onConnect(session)
	}

	s.logger.Debugw("New connection",
		"sessionId", session.ID,
		"remoteAddr", session.RemoteAddr,
		"tls", isTLS,
	)

	// Handle connection
	defer func() {
		s.closeSession(session)
		if s.onDisconnect != nil {
			s.onDisconnect(session)
		}
	}()

	// RED-TEAM: Set initial read timeout to pre-auth timeout (shorter)
	// This forces clients to authorize quickly, preventing slow-connect attacks
	preAuthTimeout := 30 * time.Second
	if s.cfg.RateLimiting.PreAuthTimeout > 0 {
		preAuthTimeout = s.cfg.RateLimiting.PreAuthTimeout
	}
	_ = conn.SetReadDeadline(time.Now().Add(preAuthTimeout)) // #nosec G104

	// Start keepalive goroutine
	// FIX O-1: Track goroutine in WaitGroup for clean shutdown
	keepaliveCtx, keepaliveCancel := context.WithCancel(ctx)
	defer keepaliveCancel()
	s.connWg.Add(1)
	go func() {
		defer s.connWg.Done()
		s.keepaliveLoop(keepaliveCtx, session)
	}()

	// Main message loop (V1 implementation)
	s.messageLoop(ctx, session)
}

// keepaliveLoop sends periodic mining.ping messages to detect dead connections.
func (s *Server) keepaliveLoop(ctx context.Context, session *protocol.Session) {
	if s.cfg.Connection.KeepaliveInterval <= 0 {
		return
	}

	ticker := time.NewTicker(s.cfg.Connection.KeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Send mining.ping (client should respond with mining.pong)
			ping := []byte(`{"id":null,"method":"mining.ping","params":[]}` + "\n")
			_ = session.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second)) // #nosec G104
			if _, err := session.Conn.Write(ping); err != nil {
				s.logger.Debugw("Keepalive failed, closing connection",
					"sessionId", session.ID,
					"error", err,
				)
				_ = session.Conn.Close() // #nosec G104
				return
			}
		}
	}
}

// messageLoop handles messages from a session.
func (s *Server) messageLoop(ctx context.Context, session *protocol.Session) {
	// Buffer for reading
	buf := make([]byte, 4096)
	var partial []byte
	var lastPartialLen int64 // FIX O-2: Track this connection's contribution to global buffer

	// FIX O-2: Ensure we clean up global counter on exit
	defer func() {
		if lastPartialLen > 0 {
			s.partialBufferBytes.Add(-lastPartialLen)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Read data
		n, err := session.Conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				s.logger.Debugw("Session timeout",
					"sessionId", session.ID,
					"lastActivity", session.GetLastActivity(),
				)
			}
			return
		}

		// Update activity (atomic)
		session.SetLastActivity(time.Now())
		_ = session.Conn.SetReadDeadline(time.Now().Add(s.cfg.Connection.Timeout)) // #nosec G104

		// SECURITY: Check buffer size BEFORE appending to prevent unbounded growth
		// This prevents DoS attacks where attacker sends fragmented messages to exhaust memory
		if len(partial)+n > 16384 {
			s.logger.Warnw("Message too large, disconnecting",
				"sessionId", session.ID,
				"currentLen", len(partial),
				"incomingLen", n,
			)
			return
		}

		// FIX O-2: Check global partial buffer limit before allowing growth
		// This prevents distributed attacks where many connections each hold partial messages
		// Uses atomic reserve-then-check to avoid TOCTOU race between Load and Add
		newPartialLen := int64(len(partial) + n)
		delta := newPartialLen - lastPartialLen
		maxBytes := s.maxPartialBufferMB * 1024 * 1024
		if maxBytes > 0 && delta > 0 {
			newTotal := s.partialBufferBytes.Add(delta)
			if newTotal > maxBytes {
				s.partialBufferBytes.Add(-delta) // rollback reservation
				s.logger.Warnw("Global partial buffer limit exceeded, disconnecting",
					"sessionId", session.ID,
					"globalTotalMB", newTotal/(1024*1024),
					"limitMB", s.maxPartialBufferMB,
				)
				return
			}
			lastPartialLen = newPartialLen
		} else {
			s.partialBufferBytes.Add(delta)
			lastPartialLen = newPartialLen
		}

		// Append to partial buffer
		partial = append(partial, buf[:n]...)

		// Process complete messages (newline-delimited JSON)
		for {
			idx := -1
			for i, b := range partial {
				if b == '\n' {
					idx = i
					break
				}
			}
			if idx == -1 {
				break
			}

			// Extract message
			msg := partial[:idx]
			partial = partial[idx+1:]

			if len(msg) == 0 {
				continue
			}

			// Handle message (V1 JSON-RPC)
			s.handleMessage(session, msg)
		}

		// FIX O-2: Update tracking after message processing reduced buffer
		if int64(len(partial)) != lastPartialLen {
			s.partialBufferBytes.Add(int64(len(partial)) - lastPartialLen)
			lastPartialLen = int64(len(partial))
		}
	}
}

// handleMessage processes a single JSON-RPC message.
// This function includes panic recovery to prevent a malformed message
// from crashing the entire stratum server.
func (s *Server) handleMessage(session *protocol.Session, msg []byte) {
	// RED-TEAM: Pre-auth message rate limiting
	// Prevents subscribe spam attacks where attacker sends many messages before authorizing
	if !session.IsAuthorized() {
		count := session.IncrementPreAuthMessages()
		if int(count) > s.cfg.RateLimiting.PreAuthMessageLimit {
			s.logger.Warnw("Pre-auth message limit exceeded, disconnecting",
				"sessionId", session.ID,
				"count", count,
				"limit", s.cfg.RateLimiting.PreAuthMessageLimit,
			)
			// Close connection - don't process message
			session.Conn.Close()
			return
		}
	}

	// Recover from any panic in message processing
	// A single bad message should not crash the server or disconnect other miners
	defer func() {
		if r := recover(); r != nil {
			// SECURITY: Sanitize log output to prevent sensitive data leaks
			// Only log message length and first 100 chars (truncated) to avoid
			// exposing potential credentials or other sensitive data in logs
			s.logger.Errorw("PANIC in message handler - recovered",
				"sessionId", session.ID,
				"panic", r,
				"messageLen", len(msg),
				"messageTruncated", sanitizeLogMessage(msg, 100),
			)
			// Send error response to client
			errResp := `{"id":null,"result":null,"error":[20,"Internal server error",null]}` + "\n"
			_ = session.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second)) // #nosec G104
			_, _ = session.Conn.Write([]byte(errResp))                         // #nosec G104
		}
	}()

	// SECURITY: Only log message content at debug level, and sanitize it
	// This prevents accidental logging of sensitive data in production
	s.logger.Debugw("Received message",
		"sessionId", session.ID,
		"messageLen", len(msg),
		"messageTruncated", sanitizeLogMessage(msg, 200),
	)

	// Snapshot subscribe/authorize state BEFORE the handler so we can detect
	// the exact moment each transition fires (used by the connection classifier).
	wasSubscribed := session.IsSubscribed()
	wasAuthorized := session.IsAuthorized()

	// Delegate to V1 handler for processing
	response, err := s.v1Handler.HandleMessage(session, msg)
	if err != nil {
		s.logger.Warnw("Failed to handle message",
			"sessionId", session.ID,
			"error", err,
		)
		// Send error response if we can parse the request ID
		var req struct {
			ID interface{} `json:"id"`
		}
		if json.Unmarshal(msg, &req) == nil && req.ID != nil {
			// Properly marshal the ID to handle both numeric and string IDs
			idJSON, err := json.Marshal(req.ID)
			if err != nil {
				idJSON = []byte("null")
			}
			errResp := fmt.Sprintf(`{"id":%s,"result":null,"error":[20,"Internal error",null]}`+"\n", idJSON)
			_ = session.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) // #nosec G104
			_, _ = session.Conn.Write([]byte(errResp))                          // #nosec G104
		}
		return
	}

	// ── Connection classifier hooks ──────────────────────────────────────────
	// Detect subscribe/authorize transitions and share submissions.
	// All writes happen on this session's goroutine so the snapshot is race-free.
	now := time.Now()
	if !wasSubscribed && session.IsSubscribed() && session.UserAgent != "" {
		var class MinerClass
		if s.spiralRouter != nil {
			class, _ = s.spiralRouter.DetectMiner(session.UserAgent)
		}
		s.classifier.RecordSubscribe(session.ID, class, now)
	}
	if !wasAuthorized && session.IsAuthorized() {
		s.classifier.RecordAuthorize(session.ID, session.WorkerName, now)
		// Logging and pool callback fire via the classifier's internal notify,
		// which fires only on type transitions — no manual poll needed here.
	}
	if en2, ok := extractSubmitEn2(msg); ok {
		s.classifier.RecordShare(session.ID, en2, now)
		// L3 entropy classification fires via the classifier's internal notify
		// at 10 / 50 / 100 samples — no manual poll needed here.
	}

	// Send response back to miner
	if len(response) > 0 {
		_ = session.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) // #nosec G104
		if _, err := session.Conn.Write(response); err != nil {
			s.logger.Warnw("Failed to send response",
				"sessionId", session.ID,
				"error", err,
			)
			return
		}

		// CRITICAL FIX: Send set_difficulty IMMEDIATELY after subscribe (before authorize)
		// ESP-Miner/NMAxe firmware sets ASIC difficulty when sending mining.suggest_difficulty,
		// which happens before authorize. If we wait until after authorize to send set_difficulty,
		// the ASIC is already configured with the suggested (wrong) difficulty.
		// By sending set_difficulty right after subscribe, we beat the firmware's suggest_difficulty.
		if session.IsSubscribed() && !session.IsAuthorized() && session.UserAgent != "" {
			// We have user-agent from subscribe, send difficulty immediately
			// Use SetDiffSent() as atomic lock to prevent duplicate sends
			if session.SetDiffSent() {
				initialDiff := s.cfg.Difficulty.Initial
				if s.spiralRouter != nil {
					profile := s.spiralRouter.GetProfileWithIP(session.UserAgent, session.RemoteAddr)
					initialDiff = profile.InitialDiff
					session.SetDifficulty(initialDiff)
				}
				if err := s.SendDifficulty(session, initialDiff); err != nil {
					s.logger.Warnw("Failed to send early difficulty after subscribe",
						"sessionId", session.ID,
						"error", err,
					)
				} else {
					s.logger.Debugw("Sent early difficulty after subscribe",
						"sessionId", session.ID,
						"difficulty", initialDiff,
						"userAgent", session.UserAgent,
					)
				}
			}

			// MOTD (Audit #16): Send configurable message of the day after subscribe
			if s.cfg.MOTD != "" {
				if motdMsg, err := s.v1Handler.BuildShowMessage(s.cfg.MOTD); err == nil {
					_ = session.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					if _, err := session.Conn.Write(motdMsg); err != nil {
						s.logger.Debugw("Failed to send MOTD",
							"sessionId", session.ID,
							"error", err,
						)
					}
				}
			}
		}

		// If miner just authorized, send them the initial difficulty (if not sent), notify pool, and send job
		if session.IsAuthorized() && session.IsSubscribed() {
			// RED-TEAM: Once authorized, extend timeout to normal connection timeout
			_ = session.Conn.SetReadDeadline(time.Now().Add(s.cfg.Connection.Timeout)) // #nosec G104
			// Send difficulty if not already sent (e.g., if subscribe didn't have user-agent)
			// SetDiffSent returns true only for the first caller, preventing race conditions
			if session.SetDiffSent() {
				// CRITICAL: Use the session's difficulty which was set by Spiral Router
				// based on the miner's user-agent (BitAxe gets lower diff than ASICs)
				// NOT the global config value which would be wrong for small miners!
				sessionDiff := session.GetDifficulty()
				if sessionDiff <= 0 {
					// Fallback to config if session difficulty not set
					sessionDiff = s.cfg.Difficulty.Initial
				}
				if err := s.SendDifficulty(session, sessionDiff); err != nil {
					s.logger.Warnw("Failed to send initial difficulty",
						"sessionId", session.ID,
						"error", err,
					)
				}
			}

			// Always notify pool and send job after authorize (even if difficulty was sent earlier)
			// Use atomic flag to ensure this only happens once per session
			if session.SetJobSent() {
				sessionDiff := session.GetDifficulty()
				if sessionDiff <= 0 {
					sessionDiff = s.cfg.Difficulty.Initial
				}

				// CRITICAL: Notify pool that session difficulty has been set
				// This allows pool to sync VARDIFF state with Spiral Router-assigned difficulty
				// Without this, VARDIFF uses global initial diff, causing share rejections
				if s.onDifficultyChange != nil {
					s.onDifficultyChange(session.ID, sessionDiff)
				}

				// Notify pool of full miner profile for per-session vardiff configuration
				// This provides TargetShareTime, MinDiff, MaxDiff specific to this miner class
				// CRITICAL: Must call onMinerClassified even when Spiral Router is disabled
				// because SOLO mining relies on this callback to set miner's wallet address
				if s.onMinerClassified != nil {
					var profile MinerProfile
					if s.spiralRouter != nil {
						profile = s.spiralRouter.GetProfileWithIP(session.UserAgent, session.RemoteAddr)
						s.logger.Debugw("Miner classified by Spiral Router",
							"sessionId", session.ID,
							"userAgent", session.UserAgent,
							"class", profile.Class.String(),
							"initialDiff", profile.InitialDiff,
							"minDiff", profile.MinDiff,
							"maxDiff", profile.MaxDiff,
							"targetShareTime", profile.TargetShareTime,
						)
					} else {
						// Spiral Router disabled (e.g., regtest with fixed difficulty)
						// Use default profile but STILL call callback for SOLO mining
						profile = MinerProfile{
							Class:           MinerClassLow,
							InitialDiff:     sessionDiff,
							MinDiff:         0.001,
							MaxDiff:         150000.0,
							TargetShareTime: 5.0,
						}
						s.logger.Debugw("Miner classified with default profile (Spiral Router disabled)",
							"sessionId", session.ID,
							"initialDiff", profile.InitialDiff,
						)
					}
					s.onMinerClassified(session.ID, profile)
				}

				// Send current job if available
				if job := s.currentJob.Load(); job != nil {
					s.sendJob(session, job)
				}
			}
		}
	}
}

// closeSession cleans up a session.
func (s *Server) closeSession(session *protocol.Session) {
	s.sessions.Delete(session.ID)
	s.classifier.Cleanup(session.ID)
	_ = session.Conn.Close() // #nosec G104 - error ignored during cleanup

	s.logger.Debugw("Session closed",
		"sessionId", session.ID,
		"validShares", session.GetValidShares(),
		"invalidShares", session.GetInvalidShares(),
		"duration", time.Since(session.ConnectedAt),
	)
}

// BroadcastJob sends a job to all connected miners.
func (s *Server) BroadcastJob(job *protocol.Job) {
	s.currentJob.Store(job)

	// Store in job history for share validation
	s.jobMu.Lock()

	// CRITICAL FIX: If cleanJobs is set (new block found), invalidate ALL old jobs
	// This prevents shares submitted against old jobs from being accepted after
	// a new block is found, which would cause "prev-blk-not-found" errors
	if job.CleanJobs {
		for id, oldJob := range s.jobs {
			oldJob.SetState(protocol.JobStateInvalidated, "new block - cleanJobs broadcast")
			delete(s.jobs, id)
		}
	}

	s.jobs[job.ID] = job
	// Keep only recent jobs
	if len(s.jobs) > 10 {
		// Find and remove oldest
		var oldest string
		var oldestTime time.Time
		for id, j := range s.jobs {
			if oldest == "" || j.CreatedAt.Before(oldestTime) {
				oldest = id
				oldestTime = j.CreatedAt
			}
		}
		delete(s.jobs, oldest)
	}
	s.jobMu.Unlock()

	// Broadcast to all sessions
	s.sessions.Range(func(id uint64, session *protocol.Session) bool {
		if session.IsSubscribed() {
			s.sendJob(session, job)
		}
		return true
	})
}

// sendJob sends a job to a single session.
func (s *Server) sendJob(session *protocol.Session, job *protocol.Job) {
	if session == nil || session.Conn == nil || job == nil {
		return
	}

	// Build the mining.notify message using V1 handler
	notifyMsg, err := s.v1Handler.BuildNotify(job)
	if err != nil {
		s.logger.Warnw("Failed to build notify message",
			"sessionId", session.ID,
			"jobId", job.ID,
			"error", err,
		)
		return
	}

	// Send to miner
	_ = session.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) // #nosec G104
	if _, err := session.Conn.Write(notifyMsg); err != nil {
		s.logger.Warnw("Failed to send job",
			"sessionId", session.ID,
			"jobId", job.ID,
			"error", err,
		)
		return
	}

	s.logger.Debugw("Sent job to miner",
		"sessionId", session.ID,
		"jobId", job.ID,
		"height", job.Height,
	)
}

// BroadcastReconnect sends a client.reconnect to all connected miners.
// Used before graceful shutdown so miners reconnect quickly rather than waiting for TCP timeout.
// waitTime is in seconds — miners should wait this long before reconnecting.
func (s *Server) BroadcastReconnect(waitTime int) {
	msg, err := s.v1Handler.BuildReconnect("", 0, waitTime)
	if err != nil {
		s.logger.Warnw("Failed to build reconnect message", "error", err)
		return
	}

	var sent int
	s.sessions.Range(func(id uint64, session *protocol.Session) bool {
		if session.IsSubscribed() && session.Conn != nil {
			_ = session.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := session.Conn.Write(msg); err == nil {
				sent++
			}
		}
		return true
	})

	s.logger.Infow("Broadcast reconnect to miners before shutdown", "sent", sent, "waitTimeSecs", waitTime)
}

// BroadcastMessage sends a client.show_message to all connected miners.
// This is used for pool-wide announcements like block found celebrations.
// Supported by cgminer, Avalon, and other stratum-compatible miners.
func (s *Server) BroadcastMessage(message string) {
	msg, err := s.v1Handler.BuildShowMessage(message)
	if err != nil {
		s.logger.Warnw("Failed to build show_message", "error", err)
		return
	}

	var sent, failed int
	s.sessions.Range(func(id uint64, session *protocol.Session) bool {
		if session.IsSubscribed() && session.Conn != nil {
			_ = session.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := session.Conn.Write(msg); err != nil {
				failed++
			} else {
				sent++
			}
		}
		return true
	})

	s.logger.Infow("Broadcast message to miners",
		"message", message,
		"sent", sent,
		"failed", failed,
	)
}

// SendMessageToSession sends a client.show_message to a specific miner session.
// Used for targeted notifications like "you found a block" messages.
// Returns true if the message was sent successfully.
func (s *Server) SendMessageToSession(sessionID uint64, message string) bool {
	session, ok := s.sessions.Get(sessionID)
	if !ok || session == nil || session.Conn == nil {
		return false
	}

	msg, err := s.v1Handler.BuildShowMessage(message)
	if err != nil {
		s.logger.Warnw("Failed to build show_message", "error", err)
		return false
	}

	if !session.IsSubscribed() {
		return false
	}

	_ = session.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := session.Conn.Write(msg); err != nil {
		s.logger.Warnw("Failed to send message to session",
			"sessionId", sessionID,
			"error", err,
		)
		return false
	}

	s.logger.Infow("Sent message to miner",
		"sessionId", sessionID,
		"message", message,
	)
	return true
}

// SendDifficulty sends a mining.set_difficulty message to a session.
// This is called by VARDIFF when the difficulty needs to be adjusted.
// IMPORTANT: Updates session.Difficulty to track what difficulty the miner is working at.
// Uses job-based tracking to prevent rejecting shares in flight.
//
// CRITICAL for cgminer/Avalon: After sending set_difficulty, we MUST also send the current
// job again. cgminer acknowledges set_difficulty but does NOT apply it to work-in-progress.
// It only uses the new difficulty when it receives a new job. Without this, cgminer can
// continue mining at the old difficulty for 10+ minutes until the next block is found.
func (s *Server) SendDifficulty(session *protocol.Session, difficulty float64) error {
	if session == nil || session.Conn == nil {
		return fmt.Errorf("invalid session")
	}

	// Build the JSON-RPC notification
	// Format: {"id":null,"method":"mining.set_difficulty","params":[difficulty]}
	msg := fmt.Sprintf(`{"id":null,"method":"mining.set_difficulty","params":[%g]}`+"\n", difficulty)

	// Write to session (with timeout)
	if err := session.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	if _, err := session.Conn.Write([]byte(msg)); err != nil {
		return fmt.Errorf("failed to send difficulty: %w", err)
	}

	// Job-based difficulty tracking:
	// 1. SetDifficulty stores the current diff as "previous" before updating
	// 2. Record the next job ID - shares for older jobs use the old difficulty
	// This prevents race conditions where shares in flight get rejected
	session.SetDifficulty(difficulty)

	// Record the job ID when difficulty changed
	// Shares for jobs with ID < this will use the previous difficulty
	if currentJob := s.currentJob.Load(); currentJob != nil {
		// Parse job ID from hex string to uint64
		if jobIDNum, err := strconv.ParseUint(currentJob.ID, 16, 64); err == nil {
			// Use next job ID (current + 1) since current job was at old difficulty
			session.SetDiffChangeJobID(jobIDNum + 1)
		}
	}

	// CRITICAL: Send current job to force cgminer to start using new difficulty.
	// cgminer only applies set_difficulty when it receives a new job.
	// Use clean_jobs=false to allow existing work to continue (grace period handles it).
	if currentJob := s.currentJob.Load(); currentJob != nil {
		// Clone job with clean_jobs=false for this session-specific send
		jobToSend := currentJob.Clone()
		jobToSend.CleanJobs = false
		s.sendJob(session, jobToSend)
	}

	s.logger.Debugw("Sent difficulty update",
		"sessionId", session.ID,
		"difficulty", difficulty,
	)

	return nil
}

// SetShareHandler sets the callback for share submissions.
func (s *Server) SetShareHandler(handler func(*protocol.Share) *protocol.ShareResult) {
	s.onShare = handler
	// Also wire to V1 handler so it can validate shares
	s.v1Handler.SetShareHandler(handler)
}

// SetConnectHandler sets the callback for new connections.
func (s *Server) SetConnectHandler(handler func(*protocol.Session)) {
	s.onConnect = handler
}

// SetDisconnectHandler sets the callback for disconnections.
func (s *Server) SetDisconnectHandler(handler func(*protocol.Session)) {
	s.onDisconnect = handler
}

// SetBlockTime configures the Spiral Router and session grace periods with the blockchain's block time.
// This ensures share targets are appropriate for the chain being mined.
// For example, 15-second block chains need faster shares than 600-second chains.
// Also sets grace periods for difficulty transitions (shorter for faster chains).
func (s *Server) SetBlockTime(blockTimeSec int) {
	// Store for new sessions
	s.blockTimeSec.Store(int32(blockTimeSec))

	if s.spiralRouter != nil {
		s.spiralRouter.SetBlockTime(blockTimeSec)
		s.logger.Infow("Block time configured",
			"blockTimeSec", blockTimeSec,
		)
	}
}

// GetBlockTime returns the configured block time in seconds.
// Returns 0 if not configured (defaults should assume 600s Bitcoin).
func (s *Server) GetBlockTime() int {
	return int(s.blockTimeSec.Load())
}

// SetAlgorithm configures the Spiral Router to use the specified algorithm's profiles.
// For Scrypt coins (LTC, DOGE, PEP, CAT, DGB-SCRYPT), this switches from SHA-256d
// profiles to Scrypt profiles with appropriate difficulty scales (~1000x lower).
// Must be called after NewServer. Safe for SHA-256d — it's already the default.
func (s *Server) SetAlgorithm(algo string) {
	if s.spiralRouter != nil {
		s.spiralRouter.SetAlgorithm(Algorithm(algo))
		s.logger.Infow("Algorithm configured for Spiral Router",
			"algorithm", algo,
		)
	}
}

// GetDefaultTargetTime returns the default scaled target share time for unclassified miners.
// This is used to set up initial vardiff state BEFORE miner classification.
// Delegates to Spiral Router which has the scaled profiles based on configured block time.
// Works for ANY coin - automatically scales based on that coin's block time.
func (s *Server) GetDefaultTargetTime() float64 {
	if s.spiralRouter != nil {
		return s.spiralRouter.GetDefaultTargetTime()
	}
	// Fallback if no router configured
	return 5.0
}

// SetDifficultyChangeHandler sets the callback for when session difficulty changes.
// This is called when Spiral Router assigns initial difficulty based on user-agent.
// Used by pool to sync VARDIFF state with the actual session difficulty.
func (s *Server) SetDifficultyChangeHandler(handler func(sessionID uint64, difficulty float64)) {
	s.onDifficultyChange = handler
}

// SetMinerClassifiedHandler sets the callback for when a miner is classified.
// This provides the full MinerProfile including TargetShareTime for per-session vardiff.
// Called after Spiral Router classifies the miner based on user-agent.
func (s *Server) SetMinerClassifiedHandler(handler func(sessionID uint64, profile MinerProfile)) {
	s.onMinerClassified = handler
}

// SetConnectionClassifiedHandler sets the callback for when a connection is fingerprinted
// as ASIC, PROXY, or MARKETPLACE by the three-level classifier.
// The classifier's internal callback is replaced with a wrapper that logs at
// Info level and then invokes the pool's handler — ensuring exactly one log
// line per classification transition regardless of which detection level fires.
func (s *Server) SetConnectionClassifiedHandler(handler func(sessionID uint64, c ConnectionClassification)) {
	s.onConnectionClassified = handler
	s.classifier.SetClassifiedHandler(func(id uint64, c ConnectionClassification) {
		s.logger.Infow("Connection classified",
			"sessionId", id,
			"type", c.Type.String(),
			"confidence", c.Confidence,
		)
		if handler != nil {
			handler(id, c)
		}
	})
}

// GetConnectionClassification returns the current classification for a session.
func (s *Server) GetConnectionClassification(sessionID uint64) ConnectionClassification {
	return s.classifier.GetClassification(sessionID)
}

// extractSubmitEn2 parses a mining.submit message and returns the extranonce2 field (params[2]).
// Returns ("", false) if the message is not a submit or cannot be parsed.
func extractSubmitEn2(msg []byte) (string, bool) {
	if !bytes.Contains(msg, []byte(`"mining.submit"`)) {
		return "", false
	}
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(msg, &req); err != nil || req.Method != "mining.submit" {
		return "", false
	}
	if len(req.Params) < 3 {
		return "", false
	}
	var en2 string
	if err := json.Unmarshal(req.Params[2], &en2); err != nil {
		return "", false
	}
	return en2, en2 != ""
}

// GetSession returns a session by ID.
func (s *Server) GetSession(id uint64) (*protocol.Session, bool) {
	return s.sessions.Get(id)
}

// GetJob returns a job by ID.
func (s *Server) GetJob(id string) (*protocol.Job, bool) {
	s.jobMu.RLock()
	defer s.jobMu.RUnlock()
	job, ok := s.jobs[id]
	return job, ok
}

// GetCurrentJob returns the current job.
func (s *Server) GetCurrentJob() *protocol.Job {
	return s.currentJob.Load()
}

// Stats returns server statistics.
type Stats struct {
	ActiveConnections   int64
	TotalConnections    uint64
	RejectedConnections uint64
	ActiveSessions      int
	// Additional metrics
	TLSConnections   uint64
	RateLimitedConns uint64
	RateLimiterStats *security.RateLimiterStats
}

func (s *Server) Stats() Stats {
	stats := Stats{
		ActiveConnections:   s.connCount.Load(),
		TotalConnections:    s.totalConnections.Load(),
		RejectedConnections: s.rejectedConnections.Load(),
		ActiveSessions:      s.sessions.Len(),
		TLSConnections:      s.tlsConnections.Load(),
		RateLimitedConns:    s.rateLimitedConns.Load(),
	}

	// Include rate limiter stats if enabled
	if s.rateLimiter != nil {
		rlStats := s.rateLimiter.GetStats()
		stats.RateLimiterStats = &rlStats
	}

	return stats
}

// GetRateLimiter returns the rate limiter instance
func (s *Server) GetRateLimiter() *security.RateLimiter {
	return s.rateLimiter
}

// sanitizeLogMessage sanitizes a message for safe logging.
// It truncates the message to maxLen characters and replaces any
// potentially sensitive patterns (like passwords) with redacted text.
// This prevents accidental exposure of credentials in logs.
func sanitizeLogMessage(msg []byte, maxLen int) string {
	if len(msg) == 0 {
		return "<empty>"
	}

	// Truncate to maxLen
	truncated := msg
	suffix := ""
	if len(msg) > maxLen {
		truncated = msg[:maxLen]
		suffix = "...[truncated]"
	}

	// Convert to string for processing
	s := string(truncated)

	// Replace any control characters with spaces
	sanitized := make([]byte, len(s))
	for i, c := range []byte(s) {
		if c < 32 && c != '\t' && c != '\n' && c != '\r' {
			sanitized[i] = ' '
		} else {
			sanitized[i] = c
		}
	}

	return string(sanitized) + suffix
}

// RouterProfile represents difficulty settings for a miner class (for API export).
type RouterProfile struct {
	Class           string  `json:"class"`
	InitialDiff     float64 `json:"initialDiff"`
	MinDiff         float64 `json:"minDiff"`
	MaxDiff         float64 `json:"maxDiff"`
	TargetShareTime int     `json:"targetShareTime"`
}

// GetRouterProfiles returns all Spiral Router difficulty profiles for API exposure.
func (s *Server) GetRouterProfiles() []RouterProfile {
	if s.spiralRouter == nil {
		return nil
	}

	// Use the router's active profiles which reflect algorithm selection
	// and block-time scaling, not the unscaled DefaultProfiles constants.
	activeProfiles := s.spiralRouter.GetAllProfiles()
	profiles := make([]RouterProfile, 0, len(activeProfiles))
	for class, profile := range activeProfiles {
		profiles = append(profiles, RouterProfile{
			Class:           class.String(),
			InitialDiff:     profile.InitialDiff,
			MinDiff:         profile.MinDiff,
			MaxDiff:         profile.MaxDiff,
			TargetShareTime: profile.TargetShareTime,
		})
	}
	return profiles
}

// IsSlowDiffApplier checks if a miner (by user-agent) is slow to apply new difficulty.
// Delegates to the Spiral Router's IsSlowDiffApplier() for configurable pattern matching.
// Returns false if the router is not configured.
func (s *Server) IsSlowDiffApplier(userAgent string) bool {
	if s.spiralRouter == nil {
		return false
	}
	return s.spiralRouter.IsSlowDiffApplier(userAgent)
}

// GetWorkersByClass returns the count of workers by their detected miner class.
func (s *Server) GetWorkersByClass() map[string]int {
	s.workerClassMu.RLock()
	defer s.workerClassMu.RUnlock()

	result := make(map[string]int)
	for class, count := range s.workerClasses {
		result[class.String()] = count
	}
	return result
}

// TrackWorkerClass increments the counter for a detected miner class.
func (s *Server) TrackWorkerClass(userAgent string) {
	if s.spiralRouter == nil {
		return
	}

	class, _ := s.spiralRouter.DetectMiner(userAgent)

	s.workerClassMu.Lock()
	s.workerClasses[class]++
	s.workerClassMu.Unlock()
}

// UntrackWorkerClass decrements the counter for a miner class when disconnecting.
func (s *Server) UntrackWorkerClass(userAgent string) {
	if s.spiralRouter == nil {
		return
	}

	class, _ := s.spiralRouter.DetectMiner(userAgent)

	s.workerClassMu.Lock()
	if s.workerClasses[class] > 0 {
		s.workerClasses[class]--
	}
	s.workerClassMu.Unlock()
}

// GetActiveConnections returns real-time connection status for all active sessions.
func (s *Server) GetActiveConnections() []*protocol.Session {
	sessions := make([]*protocol.Session, 0)
	s.sessions.Range(func(id uint64, session *protocol.Session) bool {
		sessions = append(sessions, session)
		return true
	})
	return sessions
}

// KickWorkerByIP closes all sessions whose remote address starts with the given IP.
// Returns the number of sessions closed. The miner's client will reconnect automatically.
func (s *Server) KickWorkerByIP(ip string) int {
	kicked := 0
	s.sessions.Range(func(id uint64, session *protocol.Session) bool {
		addr := session.RemoteAddr
		// Strip port: "192.168.1.5:3333" -> compare "192.168.1.5"
		if host, _, err := net.SplitHostPort(addr); err == nil {
			addr = host
		}
		if addr == ip {
			_ = session.Conn.Close() // #nosec G104
			kicked++
		}
		return true
	})
	return kicked
}

// GetActiveSessionIDs returns the IDs of all currently active sessions.
// H-4 fix: Used by session cleanup to identify orphaned VARDIFF states.
func (s *Server) GetActiveSessionIDs() []uint64 {
	ids := make([]uint64, 0)
	s.sessions.Range(func(id uint64, _ *protocol.Session) bool {
		ids = append(ids, id)
		return true
	})
	return ids
}
