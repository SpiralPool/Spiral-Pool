// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/security"
	"go.uber.org/zap"
)

// SECURITY: Input validation constants and patterns
const (
	maxUserIdentityLen = 256 // Maximum length for user identity (wallet.worker)
)

// validUserIdentity matches safe user identity characters (alphanumeric, dots, dashes, underscores, colons for BCH CashAddr)
// SECURITY: Prevents injection attacks and ensures safe logging/database storage
var validUserIdentity = regexp.MustCompile(`^[a-zA-Z0-9._:-]+$`)

// ServerConfig holds V2 server configuration
type ServerConfig struct {
	// Network
	ListenAddr string
	Port       int

	// Protocol
	ProtocolVersion uint16
	Flags           uint32 // Supported flags

	// Limits
	MaxConnections        int
	MaxChannelsPerSession int
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	PreSetupTimeout       time.Duration // Deadline for SetupConnection after handshake
	MaxPreSetupMessages   int           // Max messages before setup complete

	// Difficulty
	DefaultTargetNBits uint32
	MinTargetNBits     uint32
	MaxTargetNBits     uint32

	// ExtraNonce
	ExtraNonce2Size uint16
}

// DefaultServerConfig returns default configuration
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		ListenAddr:            "0.0.0.0",
		Port:                  DefaultPort,
		ProtocolVersion:       2,
		Flags:                 ProtocolFlagRequiresStandardJobs | ProtocolFlagRequiresVersionRolling,
		MaxConnections:        100000, // 10PH/s design point: S19 Pro 110TH = ~91K miners worst case
		MaxChannelsPerSession: 10,
		ReadTimeout:           5 * time.Minute,
		WriteTimeout:          30 * time.Second,
		PreSetupTimeout:       10 * time.Second,
		MaxPreSetupMessages:   20,
		DefaultTargetNBits:    0x1d00ffff, // Difficulty 1
		MinTargetNBits:        0x1d00ffff,
		MaxTargetNBits:        0x207fffff,
		ExtraNonce2Size:       8,
	}
}

// JobProvider is the interface for job management
type JobProvider interface {
	// GetCurrentJob returns the current mining job
	GetCurrentJob() *MiningJobData
	// GetJob returns a job by ID
	GetJob(id uint32) *MiningJobData
	// OnNewBlock is called when a new block is received
	RegisterNewBlockCallback(func())
}

// MiningJobData holds job data from the job manager
type MiningJobData struct {
	ID         uint32
	PrevHash   [32]byte
	MerkleRoot [32]byte
	Version    uint32
	NBits      uint32
	NTime      uint32
	CleanJobs  bool
}

// ShareHandler is the interface for share processing
type ShareHandler interface {
	// ProcessShare validates and records a share
	ProcessShare(share *ShareSubmission) *ShareResult
}

// ShareSubmission represents a submitted share
type ShareSubmission struct {
	SessionID   string
	ChannelID   uint32
	JobID       uint32
	Nonce       uint32
	NTime       uint32
	Version     uint32
	ExtraNonce2 []byte
	TargetNBits uint32 // Channel's share target (NOT network target)
}

// ShareResult is the result of share validation
type ShareResult struct {
	Accepted  bool
	IsBlock   bool
	BlockHash string
	Error     error
}

// Server is the Stratum V2 server
type Server struct {
	config   *ServerConfig
	logger   *zap.SugaredLogger
	listener net.Listener

	// Cryptographic keys
	serverKeys *ServerKeys

	// Session management
	sessions *SessionManager

	// External integrations
	jobProvider  JobProvider
	shareHandler ShareHandler

	// Rate limiter for DDoS protection (optional, nil = disabled)
	rateLimiter *security.RateLimiter

	// State
	running atomic.Bool
	wg      sync.WaitGroup

	// Callbacks
	onConnect    func(*Session)
	onDisconnect func(*Session)

	// Global buffer memory tracking (FIX O-2 parity with V1)
	// Prevents memory exhaustion from many connections with partial messages
	partialBufferBytes atomic.Int64
	maxPartialBufferMB int64 // Default 512MB

	// Metrics
	totalConnections    atomic.Uint64
	totalShares         atomic.Uint64
	totalBlocks         atomic.Uint64
	rateLimitedConns    atomic.Uint64
}

// NewServer creates a new V2 server
func NewServer(config *ServerConfig, logger *zap.SugaredLogger) (*Server, error) {
	if config == nil {
		config = DefaultServerConfig()
	}

	// Generate server keys
	keys, err := GenerateServerKeys()
	if err != nil {
		return nil, fmt.Errorf("failed to generate server keys: %w", err)
	}

	return &Server{
		config:             config,
		logger:             logger,
		serverKeys:         keys,
		sessions:           NewSessionManager(),
		maxPartialBufferMB: 512, // 512MB global limit for partial message buffers
	}, nil
}

// SetJobProvider sets the job provider
func (s *Server) SetJobProvider(jp JobProvider) {
	s.jobProvider = jp
	if jp != nil {
		jp.RegisterNewBlockCallback(s.broadcastNewBlock)
	}
}

// SetShareHandler sets the share handler
func (s *Server) SetShareHandler(sh ShareHandler) {
	s.shareHandler = sh
}

// SetRateLimiter sets an optional rate limiter for DDoS protection.
// If nil, rate limiting is disabled for V2 connections.
func (s *Server) SetRateLimiter(rl *security.RateLimiter) {
	s.rateLimiter = rl
}

// OnConnect sets a callback for new connections
func (s *Server) OnConnect(fn func(*Session)) {
	s.onConnect = fn
}

// OnDisconnect sets a callback for disconnections
func (s *Server) OnDisconnect(fn func(*Session)) {
	s.onDisconnect = fn
}

// Start starts the server
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", s.config.ListenAddr, s.config.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	s.listener = listener
	s.running.Store(true)

	s.logger.Infow("Stratum V2 server started",
		"addr", addr,
		"pubkey", hex.EncodeToString(s.serverKeys.Public[:]),
	)

	s.wg.Add(1)
	go s.acceptLoop(ctx)

	return nil
}

// Stop stops the server
func (s *Server) Stop() error {
	if !s.running.CompareAndSwap(true, false) {
		return nil // Already stopped
	}

	s.logger.Info("Stopping Stratum V2 server...")

	// Close listener
	if s.listener != nil {
		_ = s.listener.Close() // #nosec G104 - error ignored during shutdown
	}

	// Close all sessions
	s.sessions.CloseAll()

	// Wait for goroutines
	s.wg.Wait()

	s.logger.Info("Stratum V2 server stopped")
	return nil
}

// acceptLoop accepts new connections
func (s *Server) acceptLoop(ctx context.Context) {
	defer s.wg.Done()

	for s.running.Load() {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.running.Load() {
				s.logger.Warnw("Accept error", "error", err)
			}
			continue
		}

		// Check connection limit — accept then reject to avoid busy-wait
		if s.sessions.Count() >= int64(s.config.MaxConnections) {
			s.logger.Debugw("Max connections reached, rejecting", "limit", s.config.MaxConnections)
			_ = conn.Close() // #nosec G104
			continue
		}

		// SECURITY: Rate limiting check before handling connection
		if s.rateLimiter != nil {
			allowed, reason := s.rateLimiter.AllowConnection(conn.RemoteAddr())
			if !allowed {
				s.rateLimitedConns.Add(1)
				s.logger.Debugw("V2 connection rate limited",
					"remoteAddr", conn.RemoteAddr().String(),
					"reason", reason,
				)
				_ = conn.Close() // #nosec G104
				continue
			}
		}

		s.totalConnections.Add(1)
		s.wg.Add(1)
		go s.handleConnection(ctx, conn)
	}
}

// handleConnection handles a new connection
func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }() // #nosec G104 - error ignored during cleanup

	// SECURITY: Release rate limiter slot on disconnect
	if s.rateLimiter != nil {
		defer s.rateLimiter.ReleaseConnection(conn.RemoteAddr())
	}

	remoteAddr := conn.RemoteAddr().String()
	s.logger.Debugw("New V2 connection", "addr", remoteAddr)

	// Perform Noise handshake
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second)) // #nosec G104
	noiseConn, err := ServerHandshake(conn, s.serverKeys)
	if err != nil {
		s.logger.Warnw("Handshake failed", "addr", remoteAddr, "error", err)
		return
	}

	// FIX 2a: Pre-setup timeout — client must send SetupConnection within this deadline.
	// Without this, a client that completes the Noise handshake but never sends
	// SetupConnection will hold a session slot indefinitely.
	_ = noiseConn.conn.SetReadDeadline(time.Now().Add(s.config.PreSetupTimeout)) // #nosec G104

	// Create session
	sessionID := s.generateSessionID()
	session := NewSession(sessionID, noiseConn)

	s.sessions.Add(session)
	defer func() {
		s.sessions.Remove(sessionID)
		_ = session.Close() // #nosec G104 - error ignored during cleanup
		if s.onDisconnect != nil {
			s.onDisconnect(session)
		}
		s.logger.Debugw("V2 session closed",
			"id", sessionID,
			"duration", session.Duration(),
			"accepted", session.TotalSharesAccepted.Load(),
			"rejected", session.TotalSharesRejected.Load(),
		)
	}()

	if s.onConnect != nil {
		s.onConnect(session)
	}

	s.logger.Infow("V2 handshake complete", "id", sessionID, "addr", remoteAddr)

	// Message loop
	s.messageLoop(ctx, session)
}

// messageLoop handles messages for a session
func (s *Server) messageLoop(ctx context.Context, session *Session) {
	preSetupMessages := 0

	for !session.IsClosed() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// FIX 2d: Refresh read deadline after each message (keepalive / dead connection detection).
		// Use PreSetupTimeout until setup is complete, then switch to ReadTimeout.
		// FIX 2e: Without the state check, the 5min ReadTimeout overwrites the 10s PreSetupTimeout
		// on the first loop iteration, allowing clients to hold slots indefinitely without setup.
		if session.GetState() >= StateSetupComplete {
			_ = session.Conn.conn.SetReadDeadline(time.Now().Add(s.config.ReadTimeout)) // #nosec G104
		} else {
			_ = session.Conn.conn.SetReadDeadline(time.Now().Add(s.config.PreSetupTimeout)) // #nosec G104
		}

		// Read message header
		var header MessageHeader
		if err := header.Decode(session.Conn); err != nil {
			if err != io.EOF && !session.IsClosed() {
				s.logger.Debugw("Read error", "id", session.ID, "error", err)
			}
			return
		}

		// Validate message size
		if header.Length > MaxMessageSize {
			s.logger.Warnw("Message too large", "id", session.ID, "size", header.Length)
			return
		}

		// FIX 2c: Global buffer memory tracking — reserve-then-check before allocation.
		// Prevents memory exhaustion from many connections with large partial messages.
		if s.maxPartialBufferMB > 0 {
			maxBytes := s.maxPartialBufferMB * 1024 * 1024
			newTotal := s.partialBufferBytes.Add(int64(header.Length))
			if newTotal > maxBytes {
				s.partialBufferBytes.Add(-int64(header.Length)) // Release reservation
				s.logger.Warnw("Global partial buffer limit exceeded, disconnecting",
					"sessionId", session.ID,
					"globalTotalMB", newTotal/(1024*1024),
					"limitMB", s.maxPartialBufferMB,
				)
				return
			}
		}

		// Read payload
		payload := make([]byte, header.Length)
		if _, err := io.ReadFull(session.Conn, payload); err != nil {
			if s.maxPartialBufferMB > 0 {
				s.partialBufferBytes.Add(-int64(header.Length)) // Release on read failure
			}
			s.logger.Debugw("Payload read error", "id", session.ID, "error", err)
			return
		}

		// Release buffer tracking after payload is fully read and will be processed
		if s.maxPartialBufferMB > 0 {
			s.partialBufferBytes.Add(-int64(header.Length))
		}

		session.BytesReceived.Add(uint64(HeaderSize + header.Length))

		// FIX 2b: Pre-setup message limit — cap messages before SetupConnection completes.
		// Prevents clients from consuming server resources without completing protocol setup.
		if session.GetState() < StateSetupComplete {
			preSetupMessages++
			if preSetupMessages > s.config.MaxPreSetupMessages {
				s.logger.Warnw("Pre-setup message limit exceeded, disconnecting",
					"id", session.ID,
					"messages", preSetupMessages,
					"limit", s.config.MaxPreSetupMessages,
				)
				return
			}
		}

		// Handle message
		if err := s.handleMessage(session, header.MsgType, payload); err != nil {
			s.logger.Warnw("Message handling error",
				"id", session.ID,
				"type", header.MsgType,
				"error", err,
			)
		}

		// FIX 2a: Clear pre-setup timeout once SetupConnection succeeds.
		// After setup, the normal ReadTimeout governs keepalive detection.
		if session.GetState() >= StateSetupComplete && preSetupMessages > 0 {
			_ = session.Conn.conn.SetReadDeadline(time.Now().Add(s.config.ReadTimeout)) // #nosec G104
			preSetupMessages = 0 // Reset counter so the deadline clear only fires once
		}
	}
}

// handleMessage dispatches a message to the appropriate handler
func (s *Server) handleMessage(session *Session, msgType uint8, payload []byte) (retErr error) {
	// SECURITY: Recover from any panic in message processing
	// A single malformed message should not crash the session or the server
	defer func() {
		if r := recover(); r != nil {
			s.logger.Errorw("PANIC in V2 message handler - recovered",
				"panic", r,
				"sessionID", session.ID,
				"msgType", msgType,
				"payloadLen", len(payload),
			)
			retErr = fmt.Errorf("internal error: panic recovered")
		}
	}()

	switch msgType {
	case MsgSetupConnection:
		return s.handleSetupConnection(session, payload)
	case MsgOpenStandardMiningChannel:
		return s.handleOpenStandardMiningChannel(session, payload)
	case MsgSubmitSharesStandard:
		return s.handleSubmitSharesStandard(session, payload)
	case MsgUpdateChannel:
		return s.handleUpdateChannel(session, payload)
	case MsgCloseChannel:
		return s.handleCloseChannel(session, payload)
	default:
		s.logger.Debugw("Unknown message type", "id", session.ID, "type", msgType)
		return ErrUnknownMessage
	}
}

// handleSetupConnection handles the SetupConnection message
func (s *Server) handleSetupConnection(session *Session, payload []byte) error {
	msg, err := DecodeSetupConnection(payload)
	if err != nil {
		return err
	}

	s.logger.Debugw("SetupConnection",
		"id", session.ID,
		"vendor", msg.VendorID,
		"version", fmt.Sprintf("%d-%d", msg.MinVersion, msg.MaxVersion),
	)

	// Validate protocol version
	if msg.MaxVersion < s.config.ProtocolVersion || msg.MinVersion > s.config.ProtocolVersion {
		errResp, encErr := EncodeSetupConnectionError(&SetupConnectionError{
			Flags:     msg.Flags,
			ErrorCode: ErrCodeProtocolVersionMismatch,
		})
		if encErr == nil {
			_ = session.Send(errResp) // #nosec G104 - best effort before disconnect
		}
		return ErrHandshakeFailed
	}

	// Store session info
	session.ProtocolVersion = s.config.ProtocolVersion
	session.Flags = msg.Flags & s.config.Flags
	session.VendorID = msg.VendorID
	session.SetState(StateSetupComplete)

	// Send success response
	resp := EncodeSetupConnectionSuccess(&SetupConnectionSuccess{
		UsedVersion: s.config.ProtocolVersion,
		Flags:       session.Flags,
	})
	return session.Send(resp)
}

// handleOpenStandardMiningChannel handles channel open requests
func (s *Server) handleOpenStandardMiningChannel(session *Session, payload []byte) error {
	// SECURITY: Enforce state machine - channel can only be opened after setup complete
	// This prevents clients from skipping protocol steps or sending messages out of order
	currentState := session.GetState()
	if currentState != StateSetupComplete && currentState != StateChannelOpen && currentState != StateMining {
		s.logger.Warnw("OpenMiningChannel rejected: invalid state",
			"id", session.ID,
			"state", currentState.String(),
			"required", "setup_complete, channel_open, or mining",
		)
		return fmt.Errorf("invalid session state for OpenMiningChannel: %s", currentState.String())
	}

	msg, err := DecodeOpenStandardMiningChannel(payload)
	if err != nil {
		return err
	}

	// SECURITY: Validate nominal hashrate to prevent NaN/Inf poisoning
	// Malicious clients could send IEEE 754 special values (NaN, Inf) which would
	// propagate through difficulty calculations and corrupt pool state
	if math.IsNaN(float64(msg.NominalHashRate)) || math.IsInf(float64(msg.NominalHashRate), 0) || msg.NominalHashRate <= 0 {
		s.logger.Warnw("Invalid nominal hashrate rejected",
			"id", session.ID,
			"hashrate", msg.NominalHashRate,
		)
		return fmt.Errorf("invalid nominal hashrate: %v", msg.NominalHashRate)
	}

	// SECURITY: Validate user identity to prevent injection attacks
	if len(msg.UserIdentity) == 0 || len(msg.UserIdentity) > maxUserIdentityLen || !validUserIdentity.MatchString(msg.UserIdentity) {
		s.logger.Warnw("Invalid user identity rejected",
			"id", session.ID,
			"reason", "validation failed",
		)
		errResp, encErr := EncodeOpenMiningChannelError(&OpenMiningChannelError{
			RequestID: msg.RequestID,
			ErrorCode: ErrCodeUnknownUser,
		})
		if encErr != nil {
			return encErr
		}
		return session.Send(errResp)
	}

	s.logger.Debugw("OpenStandardMiningChannel",
		"id", session.ID,
		"user", msg.UserIdentity,
		"hashrate", msg.NominalHashRate,
	)

	// Check channel limit
	if session.ChannelCount() >= s.config.MaxChannelsPerSession {
		errResp, encErr := EncodeOpenMiningChannelError(&OpenMiningChannelError{
			RequestID: msg.RequestID,
			ErrorCode: "max-channels-exceeded",
		})
		if encErr != nil {
			return encErr
		}
		return session.Send(errResp)
	}

	// Create channel
	channel := session.AddChannel(
		msg.UserIdentity,
		msg.NominalHashRate,
		s.config.DefaultTargetNBits,
		s.config.ExtraNonce2Size,
	)

	session.SetState(StateChannelOpen)

	// Send success response
	resp, encErr := EncodeOpenStandardMiningChannelSuccess(&OpenStandardMiningChannelSuccess{
		RequestID:        msg.RequestID,
		ChannelID:        channel.ID,
		Target:           NBitsToU256(channel.TargetNBits),
		ExtranoncePrefix: channel.ExtraNonce2,
		GroupChannelID:   0,
	})
	if encErr != nil {
		return encErr
	}
	if err := session.Send(resp); err != nil {
		return err
	}

	// Send current job if available
	if s.jobProvider != nil {
		if job := s.jobProvider.GetCurrentJob(); job != nil {
			s.sendJobToChannel(session, channel, job)
		}
	}

	session.SetState(StateMining)
	return nil
}

// handleSubmitSharesStandard handles share submissions
func (s *Server) handleSubmitSharesStandard(session *Session, payload []byte) error {
	// SECURITY: Enforce state machine - shares can only be submitted when mining
	// This prevents clients from submitting shares before proper channel setup
	currentState := session.GetState()
	if currentState != StateMining && currentState != StateChannelOpen {
		s.logger.Warnw("SubmitShares rejected: invalid state",
			"id", session.ID,
			"state", currentState.String(),
			"required", "mining or channel_open",
		)
		return fmt.Errorf("invalid session state for SubmitShares: %s", currentState.String())
	}

	msg, err := DecodeSubmitSharesStandard(payload)
	if err != nil {
		return err
	}

	// SECURITY: Check share rate limiting before processing
	if s.rateLimiter != nil {
		allowed, reason := s.rateLimiter.AllowShare(session.RemoteAddr)
		if !allowed {
			s.logger.Debugw("V2 share rate limited",
				"id", session.ID,
				"reason", reason,
			)
			return s.sendShareError(session, msg.ChannelID, msg.SequenceNum, ErrCodeRateLimited)
		}
	}

	// Get channel
	channel := session.GetChannel(msg.ChannelID)
	if channel == nil {
		return s.sendShareError(session, msg.ChannelID, msg.SequenceNum, ErrCodeInvalidChannelID)
	}

	// Update sequence number
	channel.LastSequenceNum.Store(msg.SequenceNum)
	channel.LastShareTime.Store(time.Now().Unix())

	s.totalShares.Add(1)

	// Process share if handler available
	if s.shareHandler != nil {
		result := s.shareHandler.ProcessShare(&ShareSubmission{
			SessionID:   session.ID,
			ChannelID:   msg.ChannelID,
			JobID:       msg.JobID,
			Nonce:       msg.Nonce,
			NTime:       msg.NTime,
			Version:     msg.Version,
			TargetNBits: channel.TargetNBits,
		})

		if result.Accepted {
			channel.SharesAccepted.Add(1)
			session.TotalSharesAccepted.Add(1)

			if result.IsBlock {
				s.totalBlocks.Add(1)
				s.logger.Infow("BLOCK FOUND via V2!",
					"id", session.ID,
					"channel", msg.ChannelID,
					"hash", result.BlockHash,
				)
			}

			return s.sendShareSuccess(session, msg.ChannelID, msg.SequenceNum, 1, 1)
		} else {
			channel.SharesRejected.Add(1)
			session.TotalSharesRejected.Add(1)
			return s.sendShareError(session, msg.ChannelID, msg.SequenceNum, ErrCodeDifficultyNotMet)
		}
	}

	// No handler - accept by default for testing
	channel.SharesAccepted.Add(1)
	session.TotalSharesAccepted.Add(1)
	return s.sendShareSuccess(session, msg.ChannelID, msg.SequenceNum, 1, 1)
}

// handleUpdateChannel handles channel updates
// Note: Difficulty adjustment for V2 sessions is handled via SetTarget messages
func (s *Server) handleUpdateChannel(session *Session, payload []byte) error {
	// Channel updates are acknowledged; difficulty managed via SetTarget
	return nil
}

// handleCloseChannel handles channel close
func (s *Server) handleCloseChannel(session *Session, payload []byte) error {
	dec := NewDecoderFromBytes(payload)
	channelID, err := dec.ReadU32()
	if err != nil {
		return err
	}

	session.RemoveChannel(channelID)
	s.logger.Debugw("Channel closed", "id", session.ID, "channel", channelID)
	return nil
}

// sendShareSuccess sends a share acceptance
func (s *Server) sendShareSuccess(session *Session, channelID, seqNum, count uint32, diffSum uint64) error {
	resp := EncodeSubmitSharesSuccess(&SubmitSharesSuccess{
		ChannelID:           channelID,
		LastSequenceNum:     seqNum,
		NewSubmissionsCount: count,
		NewSharesSum:        diffSum,
	})
	return session.Send(resp)
}

// sendShareError sends a share rejection
func (s *Server) sendShareError(session *Session, channelID, seqNum uint32, errCode string) error {
	resp, encErr := EncodeSubmitSharesError(&SubmitSharesError{
		ChannelID:   channelID,
		SequenceNum: seqNum,
		ErrorCode:   errCode,
	})
	if encErr != nil {
		return encErr
	}
	return session.Send(resp)
}

// sendJobToChannel sends a job to a specific channel
func (s *Server) sendJobToChannel(session *Session, channel *Channel, job *MiningJobData) {
	ntime := job.NTime
	jobMsg := EncodeNewMiningJob(&NewMiningJob{
		ChannelID:  channel.ID,
		JobID:      job.ID,
		MinNTime:   &ntime, // Non-nil = active job with min ntime
		Version:    job.Version,
		MerkleRoot: job.MerkleRoot,
	})
	_ = session.Send(jobMsg) // #nosec G104 - best effort broadcast

	// Send prevhash
	prevHash := EncodeSetNewPrevHash(&SetNewPrevHash{
		ChannelID: channel.ID,
		JobID:     job.ID,
		PrevHash:  job.PrevHash,
		MinNTime:  job.NTime,
		NBits:     job.NBits,
	})
	_ = session.Send(prevHash) // #nosec G104 - best effort broadcast
}

// BroadcastNewBlock broadcasts the current job to all connected V2 sessions.
func (s *Server) BroadcastNewBlock() {
	s.broadcastNewBlock()
}

// Port returns the configured listening port.
func (s *Server) Port() int {
	return s.config.Port
}

// broadcastNewBlock broadcasts a new block to all sessions
func (s *Server) broadcastNewBlock() {
	if s.jobProvider == nil {
		return
	}

	job := s.jobProvider.GetCurrentJob()
	if job == nil {
		return
	}

	s.sessions.ForEach(func(session *Session) bool {
		for _, channel := range session.GetChannels() {
			s.sendJobToChannel(session, channel, job)
		}
		return true
	})
}

// generateSessionID generates a unique session ID
func (s *Server) generateSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // #nosec G104 - crypto/rand.Read never fails
	return hex.EncodeToString(b)
}

// Stats returns server statistics
func (s *Server) Stats() map[string]interface{} {
	sessionStats := s.sessions.Stats()
	return map[string]interface{}{
		"active_sessions":   sessionStats.ActiveSessions,
		"total_channels":    sessionStats.TotalChannels,
		"total_connections": s.totalConnections.Load(),
		"total_shares":      s.totalShares.Load(),
		"total_blocks":      s.totalBlocks.Load(),
		"shares_accepted":   sessionStats.TotalAccepted,
		"shares_rejected":   sessionStats.TotalRejected,
		"bytes_sent":        sessionStats.TotalBytesSent,
		"bytes_received":    sessionStats.TotalBytesRecv,
	}
}

// PublicKey returns the server's public key
func (s *Server) PublicKey() [DHPubKeySize]byte {
	return s.serverKeys.Public
}

// NBitsToU256 converts compact target (nBits) to a 32-byte U256 little-endian
// representation for the SV2 wire format. Uses NBitsToTarget from adapter.go.
func NBitsToU256(nBits uint32) [32]byte {
	target := NBitsToTarget(nBits)
	var u256 [32]byte
	if target != nil && target.Sign() > 0 {
		b := target.Bytes() // big-endian
		// Write to u256 in little-endian order
		for i, j := 0, len(b)-1; j >= 0 && i < 32; i, j = i+1, j-1 {
			u256[i] = b[j]
		}
	}
	return u256
}

// U256ToTarget converts a 32-byte U256 little-endian value to a big.Int target.
func U256ToTarget(u256 [32]byte) *big.Int {
	// Convert LE to BE for big.Int
	be := make([]byte, 32)
	for i := 0; i < 32; i++ {
		be[i] = u256[31-i]
	}
	return new(big.Int).SetBytes(be)
}
