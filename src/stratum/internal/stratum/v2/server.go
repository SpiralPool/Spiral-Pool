// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/security"
	"github.com/spiralpool/stratum/internal/vardiff"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// SECURITY: Input validation constants and patterns
const (
	maxUserIdentityLen = 256 // Maximum length for user identity (wallet.worker)
)

const (
	// maxChannelJobs bounds each channel's map from SV2 job IDs to pool jobs.
	maxChannelJobs = 32

	// v2SessionIDBit keeps V2 session IDs apart from V1 session IDs, which count up
	// from zero, in the share validator's per-session nonce tracker.
	v2SessionIDBit = uint64(1) << 63

	// defaultVersionRollingMask matches the share validator's BIP320 fallback mask.
	defaultVersionRollingMask = 0x1FFFE000

	// extranoncePrefixSize is the coinbase's reserved extranonce space:
	// extranonce1 (4 bytes) || extranonce2 (8 bytes).
	extranoncePrefixSize = 12

	// An extended channel's prefix is extranonce1; its miner rolls the 8 bytes of
	// extranonce2.
	extranonce1Size        = 4
	extendedExtranonceSize = extranoncePrefixSize - extranonce1Size
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

	// Limits
	MaxConnections        int
	MaxChannelsPerSession int
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	PreSetupTimeout       time.Duration // Deadline for SetupConnection after handshake
	MaxPreSetupMessages   int           // Max messages before setup complete

	// Difficulty
	InitialDifficulty float64 // Share difficulty for new channels, in V1 stratum units
	MinTargetNBits    uint32
	MaxTargetNBits    uint32

	// InitialDifficultyFor picks a channel's starting difficulty from what the
	// miner says about itself, rather than giving every device the same number
	// from config. Returning 0 means "no opinion" and keeps InitialDifficulty.
	//
	// It is injected rather than implemented here for the same reason Pipeline
	// is: the Spiral Router lives in the V1 stratum package, and reaching for it
	// from here would be an import cycle. The V1 path picks difficulty from the
	// `mining.subscribe` user agent, which V2 has no equivalent of — but V2
	// carries something better, a nominal hashrate the device reports itself,
	// and nothing was reading it.
	InitialDifficultyFor func(vendorID string, nominalHashRate float64) float64

	// Vardiff for V2 channels, used when VarDiff.Enabled. VarDiffTargetTime is the
	// seconds wanted between shares; 0 uses VarDiff.TargetTime.
	VarDiff           config.VarDiffConfig
	VarDiffTargetTime float64

	// Noise static and authority keys. When nil, NewServer generates throwaway
	// keys, which miners cannot pin across restarts.
	Keys *ServerKeys
}

// DefaultServerConfig returns default configuration
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		ListenAddr:            "0.0.0.0",
		Port:                  DefaultPort,
		ProtocolVersion:       2,
		MaxConnections:        100000, // 10PH/s design point: S19 Pro 110TH = ~91K miners worst case
		MaxChannelsPerSession: 10,
		ReadTimeout:           5 * time.Minute,
		WriteTimeout:          30 * time.Second,
		PreSetupTimeout:       10 * time.Second,
		MaxPreSetupMessages:   20,
		InitialDifficulty:     1,
		MinTargetNBits:        0x1d00ffff,
		MaxTargetNBits:        0x207fffff,
	}
}

// Pipeline connects V2 channels to the pool's V1 job and share pipeline, so V2
// miners work on the same jobs, and their shares go through the same validation,
// block submission and share recording, as V1 miners.
type Pipeline struct {
	// CurrentJob returns the job manager's current job.
	CurrentJob func() *protocol.Job
	// MerkleRoot returns the header merkle root for the coinbase a share builds:
	// CoinBase1 || ExtraNonce1 || ExtraNonce2 || CoinBase2For(MinerAddress).
	MerkleRoot func(job *protocol.Job, share *protocol.Share) ([]byte, error)
	// ShareTarget converts a share difficulty to the target the validator enforces.
	ShareTarget func(difficulty float64) *big.Int
	// SubmitShare validates a share and, when it is a block, submits it.
	SubmitShare func(share *protocol.Share) *protocol.ShareResult
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

	// Pool job and share pipeline (nil until SetPipeline)
	pipeline *Pipeline

	// Vardiff engine for channels (nil when vardiff is off)
	vardiff *vardiff.Engine

	// Explains the first channel whose difficulty the miner dictated. Once per
	// server, not per channel: it is orientation for the operator, not an event.
	minerFloorNotice sync.Once

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

	// Unique numeric session IDs and per-channel extranonce prefixes
	nextSessionID atomic.Uint64
	prefixCounter atomic.Uint64
	prefixTag     [3]byte

	// Metrics
	totalConnections atomic.Uint64
	totalShares      atomic.Uint64
	totalBlocks      atomic.Uint64
	rateLimitedConns atomic.Uint64
}

// NewServer creates a new V2 server
func NewServer(config *ServerConfig, logger *zap.SugaredLogger) (*Server, error) {
	if config == nil {
		config = DefaultServerConfig()
	}

	keys := config.Keys
	if keys == nil {
		var err error
		if keys, err = GenerateServerKeys(); err != nil {
			return nil, fmt.Errorf("failed to generate server keys: %w", err)
		}
	}

	s := &Server{
		config:             config,
		logger:             logger,
		serverKeys:         keys,
		sessions:           NewSessionManager(),
		maxPartialBufferMB: 512, // 512MB global limit for partial message buffers
	}
	if config.VarDiff.Enabled {
		s.vardiff = vardiff.NewEngine(config.VarDiff)
	}
	_, _ = rand.Read(s.prefixTag[:]) // #nosec G104 - crypto/rand.Read never fails
	return s, nil
}

// SetPipeline connects the server to the pool's job and share pipeline.
func (s *Server) SetPipeline(p *Pipeline) {
	s.pipeline = p
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

	authority := s.serverKeys.AuthorityPublicKey()
	s.logger.Infow("Stratum V2 server started",
		"addr", addr,
		"authorityPubkey", hex.EncodeToString(authority[:]),
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
	session.NumericID = v2SessionIDBit | s.nextSessionID.Add(1)

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

		// Messages of protocol extensions this server does not implement are ignored.
		if ext := header.ExtensionType &^ ChannelMsgBit; ext != 0 {
			s.logger.Debugw("Ignoring extension message", "id", session.ID, "extension", ext, "type", header.MsgType)
			continue
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
	case MsgOpenExtendedMiningChannel:
		return s.handleOpenExtendedMiningChannel(session, payload)
	case MsgSubmitSharesStandard:
		return s.handleSubmitSharesStandard(session, payload)
	case MsgSubmitSharesExtended:
		return s.handleSubmitSharesExtended(session, payload)
	case MsgUpdateChannel:
		return s.handleUpdateChannel(session, payload)
	case MsgCloseChannel:
		return s.handleCloseChannel(session, payload)
	default:
		s.logger.Debugw("Unknown message type", "id", session.ID, "type", msgType)
		return ErrUnknownMessage
	}
}

// requireState rejects a message that arrives in the wrong protocol state.
// SECURITY: prevents clients from skipping protocol steps or sending messages out of order.
func (s *Server) requireState(session *Session, what string, allowed ...SessionState) error {
	state := session.GetState()
	for _, a := range allowed {
		if state == a {
			return nil
		}
	}
	s.logger.Warnw(what+" rejected: invalid state", "id", session.ID, "state", state.String())
	return fmt.Errorf("invalid session state for %s: %s", what, state.String())
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
		"device", msg.DeviceID,
		"version", fmt.Sprintf("%d-%d", msg.MinVersion, msg.MaxVersion),
		"flags", msg.Flags,
	)

	switch {
	case msg.Protocol != ProtocolMiningV2:
		return s.refuseSetup(session, 0, ErrCodeUnsupportedProtocol)
	case msg.MaxVersion < s.config.ProtocolVersion || msg.MinVersion > s.config.ProtocolVersion:
		return s.refuseSetup(session, 0, ErrCodeProtocolVersionMismatch)
	case msg.Flags&ProtocolFlagRequiresWorkSelection != 0:
		// Custom jobs need the Job Declaration protocol, which this pool does not run.
		return s.refuseSetup(session, ProtocolFlagRequiresWorkSelection, ErrCodeUnsupportedFeatureFlags)
	}

	session.ProtocolVersion = s.config.ProtocolVersion
	session.Flags = msg.Flags
	session.VendorID = msg.VendorID
	session.DeviceID = msg.DeviceID
	if err := session.TransitionTo(StateSetupComplete); err != nil {
		return fmt.Errorf("setup connection rejected: %w", err)
	}

	// Success flags are the server's own requirements, not an echo of the client's:
	// this pool requires neither a fixed version (bit 0) nor extended channels (bit 1).
	resp := EncodeSetupConnectionSuccess(&SetupConnectionSuccess{
		UsedVersion: s.config.ProtocolVersion,
		Flags:       0,
	})
	return session.Send(resp)
}

// refuseSetup sends SetupConnection.Error, naming any unsupported flags, and closes
// the connection.
func (s *Server) refuseSetup(session *Session, flags uint32, code string) error {
	if resp, err := EncodeSetupConnectionError(&SetupConnectionError{Flags: flags, ErrorCode: code}); err == nil {
		_ = session.Send(resp) // #nosec G104 - best effort before disconnect
	}
	_ = session.Close() // #nosec G104
	return fmt.Errorf("setup refused: %s", code)
}

// handleOpenStandardMiningChannel handles standard channel open requests
func (s *Server) handleOpenStandardMiningChannel(session *Session, payload []byte) error {
	if err := s.requireState(session, "OpenMiningChannel", StateSetupComplete, StateChannelOpen, StateMining); err != nil {
		return err
	}
	msg, err := DecodeOpenStandardMiningChannel(payload)
	if err != nil {
		return err
	}
	return s.openChannel(session, msg, false, 0)
}

// handleOpenExtendedMiningChannel handles extended channel open requests
func (s *Server) handleOpenExtendedMiningChannel(session *Session, payload []byte) error {
	if err := s.requireState(session, "OpenMiningChannel", StateSetupComplete, StateChannelOpen, StateMining); err != nil {
		return err
	}
	msg, err := DecodeOpenExtendedMiningChannel(payload)
	if err != nil {
		return err
	}
	return s.openChannel(session, &msg.OpenStandardMiningChannel, true, msg.MinExtranonceSize)
}

// openChannel opens a standard or extended channel and sends it the current job.
func (s *Server) openChannel(session *Session, msg *OpenStandardMiningChannel, extended bool, minExtranonceSize uint16) error {
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
		return s.sendOpenChannelError(session, msg.RequestID, ErrCodeUnknownUser)
	}

	s.logger.Debugw("OpenMiningChannel",
		"id", session.ID,
		"user", msg.UserIdentity,
		"hashrate", msg.NominalHashRate,
		"extended", extended,
	)

	// Check channel limit
	if session.ChannelCount() >= s.config.MaxChannelsPerSession {
		return s.sendOpenChannelError(session, msg.RequestID, "max-channels-exceeded")
	}

	if extended && int(minExtranonceSize) > extendedExtranonceSize {
		return s.sendOpenChannelError(session, msg.RequestID, ErrCodeUnsupportedMinExtranonceSize)
	}

	// What the miner declared about itself beats a single config number applied
	// to every device: OpenMiningChannel carries nominal_hash_rate, and the
	// SetupConnection vendor string is already on the session.
	difficulty := s.config.InitialDifficulty
	if s.config.InitialDifficultyFor != nil {
		if d := s.config.InitialDifficultyFor(session.VendorID, float64(msg.NominalHashRate)); d > 0 {
			difficulty = d
		}
	}
	if difficulty <= 0 {
		difficulty = 1
	}
	var target [32]byte
	if s.pipeline != nil && s.pipeline.ShareTarget != nil {
		target = targetToU256(s.pipeline.ShareTarget(difficulty))
	}

	// max_target is the easiest target the client can use. A starting target above it
	// is lowered to it rather than refusing the channel, as the Stratum Reference
	// Implementation's pool does: its translator sends a max_target derived from its
	// hashrate. An all-zero max_target means the client set no limit.
	maxTarget := U256ToTarget(msg.MaxTarget)
	chosen, clamped := difficulty, false
	if maxTarget.Sign() > 0 && U256ToTarget(target).Cmp(maxTarget) > 0 {
		target = targetToU256(maxTarget)
		difficulty = s.difficultyForTarget(maxTarget)
		clamped = true
	}

	// An operator who has just turned Stratum V2 on, seen a difficulty nothing in
	// their config asks for, and started editing config to change it, is about to
	// waste an evening: under V2 the miner declares the easiest target it will
	// accept and the specification requires the pool to respect it. Say so the
	// first time it happens, in the terms the operator is thinking in.
	if clamped {
		s.minerFloorNotice.Do(func() {
			s.logger.Infow("Stratum V2 difficulty is set by the miner, not the pool",
				"detail", "This miner sent a max_target — the easiest target it will accept — and SV2 requires the pool to honour it. "+
					"The pool's own choice was overridden, so stratum.difficulty.initial and the Spiral Router cannot lower it. "+
					"Change it on the miner (a suggested/starting difficulty setting, where its firmware has one). "+
					"For solo mining this is cosmetic: share difficulty does not change the odds of finding a block, only how often shares are reported.",
				"poolChose", chosen,
				"minerRequires", difficulty,
			)
		})
	}

	// Say where the channel's difficulty came from. A miner sitting at a
	// difficulty nobody configured is otherwise unattributable: the pool's own
	// choice and the floor the device demanded are both invisible, and the only
	// number anyone can see is the one that came out of the two.
	s.logger.Infow("V2 channel difficulty",
		"id", session.ID,
		"vendor", session.VendorID,
		"nominalHashRate", msg.NominalHashRate,
		"poolChose", chosen,
		"minerMaxTargetSet", maxTarget.Sign() > 0,
		"clampedToMinerFloor", clamped,
		"difficulty", difficulty,
	)

	prefix := s.nextExtranoncePrefix()
	if extended {
		prefix = s.nextExtendedPrefix()
	}
	channel := newChannel(msg.UserIdentity, msg.NominalHashRate, difficulty, target, prefix)
	if maxTarget.Sign() > 0 {
		channel.MaxTarget = maxTarget
	}
	if extended {
		channel.Extended = true
		channel.ExtranonceSize = extendedExtranonceSize
	}
	if s.vardiff != nil {
		channel.vardiff = s.vardiff.NewSessionStateWithProfile(difficulty,
			s.config.VarDiff.MinDiff, s.config.VarDiff.MaxDiff, s.config.VarDiffTargetTime)
	}
	// A job broadcast sends to every channel in the session, through the channel's
	// job lock. Holding that lock until the Success is written keeps a broadcast
	// from reaching the client before the channel it names.
	channel.jobsMu.Lock()
	err := func() error {
		defer channel.jobsMu.Unlock()
		session.addChannel(channel)

		if err := session.TransitionTo(StateChannelOpen); err != nil {
			return fmt.Errorf("open channel rejected: %w", err)
		}

		var resp []byte
		var encErr error
		if extended {
			resp, encErr = EncodeOpenExtendedMiningChannelSuccess(&OpenExtendedMiningChannelSuccess{
				RequestID:        msg.RequestID,
				ChannelID:        channel.ID,
				Target:           target,
				ExtranonceSize:   uint16(extendedExtranonceSize),
				ExtranoncePrefix: prefix,
				GroupChannelID:   0,
			})
		} else {
			resp, encErr = EncodeOpenStandardMiningChannelSuccess(&OpenStandardMiningChannelSuccess{
				RequestID:        msg.RequestID,
				ChannelID:        channel.ID,
				Target:           target,
				ExtranoncePrefix: prefix,
				GroupChannelID:   0,
			})
		}
		if encErr != nil {
			return encErr
		}
		return session.Send(resp)
	}()
	if err != nil {
		return err
	}

	// Send current job if available
	if s.pipeline != nil && s.pipeline.CurrentJob != nil {
		if job := s.pipeline.CurrentJob(); job != nil {
			if err := s.sendJobToChannel(session, channel, job); err != nil {
				s.logger.Warnw("V2 initial job send failed", "id", session.ID, "channel", channel.ID, "error", err)
			}
		}
	}

	if err := session.TransitionTo(StateMining); err != nil {
		return fmt.Errorf("mining transition rejected: %w", err)
	}
	return nil
}

// sendOpenChannelError sends OpenMiningChannel.Error
func (s *Server) sendOpenChannelError(session *Session, requestID uint32, errCode string) error {
	errResp, encErr := EncodeOpenMiningChannelError(&OpenMiningChannelError{
		RequestID: requestID,
		ErrorCode: errCode,
	})
	if encErr != nil {
		return encErr
	}
	return session.Send(errResp)
}

// handleSubmitSharesStandard handles share submissions on standard channels
func (s *Server) handleSubmitSharesStandard(session *Session, payload []byte) error {
	if err := s.requireState(session, "SubmitShares", StateMining, StateChannelOpen); err != nil {
		return err
	}
	msg, err := DecodeSubmitSharesStandard(payload)
	if err != nil {
		return err
	}
	return s.submitShare(session, msg.ChannelID, msg.SequenceNum, msg.JobID, false,
		func(ch *Channel, job channelJob) (*protocol.Share, string) {
			return newShare(session, ch, job, msg.Nonce, msg.NTime, msg.Version, ch.ExtranoncePrefix[extranonce1Size:]), ""
		})
}

// handleSubmitSharesExtended handles share submissions on extended channels
func (s *Server) handleSubmitSharesExtended(session *Session, payload []byte) error {
	if err := s.requireState(session, "SubmitShares", StateMining, StateChannelOpen); err != nil {
		return err
	}
	msg, err := DecodeSubmitSharesExtended(payload)
	if err != nil {
		return err
	}
	return s.submitShare(session, msg.ChannelID, msg.SequenceNum, msg.JobID, true,
		func(ch *Channel, job channelJob) (*protocol.Share, string) {
			if len(msg.Extranonce) != ch.ExtranonceSize {
				return nil, ErrCodeInvalidExtranonceSize
			}
			return newShare(session, ch, job, msg.Nonce, msg.NTime, msg.Version, msg.Extranonce), ""
		})
}

// submitShare runs a share through the pool pipeline. build turns the submission
// into a pool share, or returns an error code to reject it with.
func (s *Server) submitShare(session *Session, channelID, seq, jobID uint32, extended bool,
	build func(*Channel, channelJob) (*protocol.Share, string)) error {
	// SECURITY: Check share rate limiting before processing
	if s.rateLimiter != nil {
		allowed, reason := s.rateLimiter.AllowShare(session.RemoteAddr)
		if !allowed {
			s.logger.Debugw("V2 share rate limited",
				"id", session.ID,
				"reason", reason,
			)
			return s.sendShareError(session, channelID, seq, ErrCodeRateLimited)
		}
	}

	// A standard submission for an extended channel, or the reverse, names no
	// channel of the right kind.
	channel := session.GetChannel(channelID)
	if channel == nil || channel.Extended != extended {
		return s.sendShareError(session, channelID, seq, ErrCodeInvalidChannelID)
	}

	channel.LastSequenceNum.Store(seq)
	channel.LastShareTime.Store(time.Now().Unix())
	s.totalShares.Add(1)

	reject := func(code string) error {
		channel.SharesRejected.Add(1)
		session.TotalSharesRejected.Add(1)
		return s.sendShareError(session, channelID, seq, code)
	}

	job, ok := channel.lookupJob(jobID)
	if !ok || s.pipeline == nil || s.pipeline.SubmitShare == nil {
		return reject(ErrCodeInvalidJobID)
	}
	share, code := build(channel, job)
	if share == nil {
		return reject(code)
	}
	result := s.pipeline.SubmitShare(share)
	if result == nil || !result.Accepted {
		return reject(shareErrorCode(result))
	}

	channel.SharesAccepted.Add(1)
	session.TotalSharesAccepted.Add(1)

	if result.IsBlock {
		s.totalBlocks.Add(1)
		s.logger.Infow("BLOCK FOUND via V2!",
			"id", session.ID,
			"channel", channelID,
			"miner", channel.MinerAddress,
			"worker", channel.WorkerName,
			"hash", result.BlockHash,
		)
	}

	if err := s.sendShareSuccess(session, channelID, seq, 1, shareSum(job.difficulty)); err != nil {
		return err
	}
	s.recordVardiffShare(session, channel)
	return nil
}

// shareSum is a share's contribution to SubmitShares.Success's new_shares_sum.
func shareSum(difficulty float64) uint64 {
	if difficulty < 1 || math.IsNaN(difficulty) {
		return 1
	}
	if difficulty >= math.MaxUint64 {
		return math.MaxUint64
	}
	return uint64(difficulty)
}

// newShare builds the pool share for a submission. extranonce2 is a standard
// channel's fixed prefix tail, or the extended channel miner's extranonce, so the
// validator rebuilds exactly the coinbase the miner hashed.
func newShare(session *Session, ch *Channel, job channelJob, nonce, ntime, version uint32, extranonce2 []byte) *protocol.Share {
	// The validator ORs rolled bits onto the job's base version, so pass only the
	// bits inside the rolling mask.
	var versionBits uint32
	if job.versionRolling {
		mask := job.versionMask
		if mask == 0 {
			mask = defaultVersionRollingMask
		}
		versionBits = version & mask
	}

	ip := ""
	if session.RemoteAddr != nil {
		ip = session.RemoteAddr.String()
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
	}

	return &protocol.Share{
		SessionID:     session.NumericID,
		JobID:         job.poolJobID,
		MinerAddress:  ch.MinerAddress,
		WorkerName:    ch.WorkerName,
		ExtraNonce1:   hex.EncodeToString(ch.ExtranoncePrefix[:extranonce1Size]),
		ExtraNonce2:   hex.EncodeToString(extranonce2),
		NTime:         fmt.Sprintf("%08x", ntime),
		Nonce:         fmt.Sprintf("%08x", nonce),
		VersionBits:   versionBits,
		Difficulty:    job.difficulty,
		MinDifficulty: job.difficulty,
		IPAddress:     ip,
		UserAgent:     session.VendorID,
		SubmittedAt:   time.Now(),
	}
}

// shareErrorCode maps a validator reject reason to a SubmitShares.Error code.
func shareErrorCode(result *protocol.ShareResult) string {
	if result == nil {
		return "invalid-share"
	}
	switch result.RejectReason {
	case protocol.RejectReasonInvalidJob:
		return ErrCodeInvalidJobID
	case protocol.RejectReasonStale:
		return ErrCodeStaleShare
	case protocol.RejectReasonLowDifficulty:
		return ErrCodeDifficultyTooLow
	case "":
		return "invalid-share"
	default:
		return result.RejectReason
	}
}

// recordVardiffShare feeds an accepted share to the channel's vardiff, and
// retargets the channel when its difficulty changes.
func (s *Server) recordVardiffShare(session *Session, ch *Channel) {
	if s.vardiff == nil || ch.vardiff == nil {
		return
	}
	if difficulty, changed := s.vardiff.RecordShare(ch.vardiff); changed {
		if err := s.retarget(session, ch, difficulty); err != nil {
			s.logger.Debugw("V2 retarget failed", "id", session.ID, "channel", ch.ID, "error", err)
		}
	}
}

// retarget moves a channel to a new share difficulty. It sends SetTarget, then the
// current job again, so the miner works at the new target straight away; shares
// on jobs sent earlier still count at the difficulty those jobs were sent with. A
// target easier than the channel's maximum target is capped at the maximum.
func (s *Server) retarget(session *Session, ch *Channel, difficulty float64) error {
	if s.pipeline == nil || s.pipeline.ShareTarget == nil {
		return errors.New("no job pipeline")
	}
	target := s.pipeline.ShareTarget(difficulty)
	if ch.MaxTarget != nil && target.Cmp(ch.MaxTarget) > 0 {
		target = new(big.Int).Set(ch.MaxTarget)
		difficulty = s.difficultyForTarget(target)
		if ch.vardiff != nil {
			vardiff.SetDifficulty(ch.vardiff, difficulty)
		}
	}

	ch.jobsMu.Lock()
	ch.Difficulty = difficulty
	ch.Target = targetToU256(target)
	wireTarget := ch.Target
	ch.jobsMu.Unlock()

	if err := session.Send(EncodeSetTarget(&SetTarget{ChannelID: ch.ID, MaxTarget: wireTarget})); err != nil {
		return err
	}
	if s.pipeline.CurrentJob != nil {
		if job := s.pipeline.CurrentJob(); job != nil {
			return s.sendJobToChannel(session, ch, job)
		}
	}
	return nil
}

// difficultyForTarget is the share difficulty whose target is at least target:
// the inverse of ShareTarget, rounded down so the validator never demands more
// than the target sent to the miner.
func (s *Server) difficultyForTarget(target *big.Int) float64 {
	one := s.pipeline.ShareTarget(1)
	if one == nil || target.Sign() <= 0 {
		return s.config.InitialDifficulty
	}
	d, _ := new(big.Float).Quo(new(big.Float).SetInt(one), new(big.Float).SetInt(target)).Float64()
	return d * (1 - 1e-9)
}

// handleUpdateChannel records a channel's new nominal hashrate and maximum target.
// When the channel's target is easier than the new maximum, the channel is moved
// to the maximum with SetTarget, as the spec requires.
func (s *Server) handleUpdateChannel(session *Session, payload []byte) error {
	if err := s.requireState(session, "UpdateChannel", StateChannelOpen, StateMining); err != nil {
		return err
	}
	msg, err := DecodeUpdateChannel(payload)
	if err != nil {
		return err
	}
	ch := session.GetChannel(msg.ChannelID)
	if ch == nil {
		resp, encErr := EncodeUpdateChannelError(&UpdateChannelError{ChannelID: msg.ChannelID, ErrorCode: ErrCodeInvalidChannelID})
		if encErr != nil {
			return encErr
		}
		return session.Send(resp)
	}

	if hr := float64(msg.NominalHashRate); !math.IsNaN(hr) && !math.IsInf(hr, 0) && hr > 0 {
		ch.NominalHashRate = msg.NominalHashRate
	}
	maxTarget := U256ToTarget(msg.MaximumTarget)
	if maxTarget.Sign() == 0 {
		return nil
	}
	ch.MaxTarget = maxTarget

	ch.jobsMu.Lock()
	current := U256ToTarget(ch.Target)
	ch.jobsMu.Unlock()
	if current.Cmp(maxTarget) <= 0 || s.pipeline == nil || s.pipeline.ShareTarget == nil {
		return nil
	}
	return s.retarget(session, ch, s.difficultyForTarget(maxTarget))
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

// sendJobToChannel sends a pool job to a channel. A standard channel is sent the
// merkle root of its own coinbase — its extranonce prefix and the payout address in
// its user identity. An extended channel is sent that coinbase's prefix and suffix
// around the extranonce, and the merkle path, to build the root itself. Either
// way it is the coinbase the validator rebuilds for the channel's shares.
func (s *Server) sendJobToChannel(session *Session, ch *Channel, job *protocol.Job) error {
	if s.pipeline == nil {
		return errors.New("no job pipeline")
	}
	version, err := parseHexU32(job.Version)
	if err != nil {
		return fmt.Errorf("job %s version: %w", job.ID, err)
	}
	nbits, err := parseHexU32(job.NBits)
	if err != nil {
		return fmt.Errorf("job %s nbits: %w", job.ID, err)
	}
	ntime, err := parseHexU32(job.NTime)
	if err != nil {
		return fmt.Errorf("job %s ntime: %w", job.ID, err)
	}
	prevHash, err := headerPrevHash(job.PrevBlockHash)
	if err != nil {
		return fmt.Errorf("job %s prevhash: %w", job.ID, err)
	}

	var encodeJob func(jobID uint32, minNTime *uint32) ([]byte, error)
	if ch.Extended {
		prefix, err := hex.DecodeString(job.CoinBase1)
		if err != nil {
			return fmt.Errorf("job %s coinbase1: %w", job.ID, err)
		}
		suffix, err := hex.DecodeString(job.CoinBase2For(ch.MinerAddress))
		if err != nil {
			return fmt.Errorf("job %s coinbase2: %w", job.ID, err)
		}
		path := make([][]byte, len(job.MerkleBranches))
		for i, branch := range job.MerkleBranches {
			if path[i], err = hex.DecodeString(branch); err != nil || len(path[i]) != 32 {
				return fmt.Errorf("job %s merkle branch %d is not a 32-byte hash", job.ID, i)
			}
		}
		encodeJob = func(jobID uint32, minNTime *uint32) ([]byte, error) {
			return EncodeNewExtendedMiningJob(&NewExtendedMiningJob{
				ChannelID:             ch.ID,
				JobID:                 jobID,
				MinNTime:              minNTime,
				Version:               version,
				VersionRollingAllowed: job.VersionRollingAllowed,
				MerklePath:            path,
				CoinbaseTxPrefix:      prefix,
				CoinbaseTxSuffix:      suffix,
			})
		}
	} else {
		if s.pipeline.MerkleRoot == nil {
			return errors.New("no merkle root builder")
		}
		root, err := s.pipeline.MerkleRoot(job, &protocol.Share{
			JobID:        job.ID,
			MinerAddress: ch.MinerAddress,
			ExtraNonce1:  hex.EncodeToString(ch.ExtranoncePrefix[:extranonce1Size]),
			ExtraNonce2:  hex.EncodeToString(ch.ExtranoncePrefix[extranonce1Size:]),
		})
		if err != nil {
			return fmt.Errorf("job %s merkle root: %w", job.ID, err)
		}
		if len(root) != 32 {
			return fmt.Errorf("job %s merkle root: %d bytes", job.ID, len(root))
		}
		var merkleRoot [32]byte
		copy(merkleRoot[:], root)
		encodeJob = func(jobID uint32, minNTime *uint32) ([]byte, error) {
			return EncodeNewMiningJob(&NewMiningJob{
				ChannelID:  ch.ID,
				JobID:      jobID,
				MinNTime:   minNTime,
				Version:    version,
				MerkleRoot: merkleRoot,
			}), nil
		}
	}

	// Hold the channel's job lock across both sends so concurrent broadcasts cannot
	// interleave a job with another job's SetNewPrevHash.
	ch.jobsMu.Lock()
	defer ch.jobsMu.Unlock()

	jobID := ch.addJob(channelJob{
		poolJobID:      job.ID,
		versionRolling: job.VersionRollingAllowed,
		versionMask:    job.VersionRollingMask,
		difficulty:     ch.Difficulty,
	})
	newPrevHash := job.PrevBlockHash != ch.prevHash

	var minNTime *uint32
	if !newPrevHash {
		minNTime = &ntime // active now, on the prevhash the channel already has
	}
	msg, err := encodeJob(jobID, minNTime)
	if err != nil {
		return fmt.Errorf("job %s: %w", job.ID, err)
	}
	if err := session.Send(msg); err != nil {
		return err
	}

	if newPrevHash {
		// A future job becomes active with SetNewPrevHash.
		if err := session.Send(EncodeSetNewPrevHash(&SetNewPrevHash{
			ChannelID: ch.ID,
			JobID:     jobID,
			PrevHash:  prevHash,
			MinNTime:  ntime,
			NBits:     nbits,
		})); err != nil {
			return err
		}
		ch.prevHash = job.PrevBlockHash
	}

	session.CurrentJobID.Store(jobID)
	session.LastJobSentAt.Store(time.Now().Unix())
	return nil
}

// BroadcastJob sends a pool job to every open channel. The coin pool calls it from
// the job manager's callback, so V2 miners get each new job when V1 miners do.
func (s *Server) BroadcastJob(job *protocol.Job) {
	if job == nil {
		return
	}
	s.sessions.ForEach(func(session *Session) bool {
		for _, channel := range session.GetChannels() {
			if err := s.sendJobToChannel(session, channel, job); err != nil {
				s.logger.Debugw("V2 job send failed", "id", session.ID, "channel", channel.ID, "error", err)
			}
		}
		return true
	})
}

// Port returns the configured listening port.
func (s *Server) Port() int {
	return s.config.Port
}

// generateSessionID generates a unique session ID
func (s *Server) generateSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // #nosec G104 - crypto/rand.Read never fails
	return hex.EncodeToString(b)
}

// nextExtranoncePrefix returns a process-unique 12-byte extranonce prefix for a
// standard channel.
func (s *Server) nextExtranoncePrefix() []byte {
	prefix := make([]byte, extranoncePrefixSize)
	// High bits 10: V1 extranonce1 values count up from zero.
	prefix[0] = 0x80
	copy(prefix[1:4], s.prefixTag[:])
	binary.BigEndian.PutUint64(prefix[4:], s.prefixCounter.Add(1))
	return prefix
}

// nextExtendedPrefix returns a process-unique 4-byte extranonce1 for an extended
// channel. Its high bits 11 keep it apart from standard channels and V1 sessions.
func (s *Server) nextExtendedPrefix() []byte {
	prefix := make([]byte, extranonce1Size)
	binary.BigEndian.PutUint32(prefix, 0xC0000000|uint32(s.prefixCounter.Add(1)&0x3FFFFFFF))
	return prefix
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

// AuthorityPublicKey returns the x-only key miners use to authenticate this server.
func (s *Server) AuthorityPublicKey() [32]byte {
	return s.serverKeys.AuthorityPublicKey()
}

// parseHexU32 parses a pool job's 8-character big-endian hex field.
func parseHexU32(s string) (uint32, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return 0, err
	}
	if len(b) != 4 {
		return 0, fmt.Errorf("want 4 bytes, got %d", len(b))
	}
	return binary.BigEndian.Uint32(b), nil
}

// headerPrevHash converts a pool job's stratum-format prevhash (4-byte groups in
// reversed order) to block header byte order by byte-swapping each group, the same
// conversion the share validator applies when it builds the header.
func headerPrevHash(stratumHex string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(stratumHex)
	if err != nil {
		return out, err
	}
	if len(b) != 32 {
		return out, fmt.Errorf("want 32 bytes, got %d", len(b))
	}
	for i := 0; i < 32; i += 4 {
		out[i], out[i+1], out[i+2], out[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return out, nil
}

// targetToU256 encodes a target as the SV2 wire format's 32-byte little-endian U256.
// Targets of 2^256 or more saturate to all ones.
func targetToU256(target *big.Int) [32]byte {
	var u256 [32]byte
	if target == nil || target.Sign() <= 0 {
		return u256
	}
	if target.BitLen() > 256 {
		for i := range u256 {
			u256[i] = 0xff
		}
		return u256
	}
	b := target.Bytes() // big-endian
	for i, j := 0, len(b)-1; j >= 0; i, j = i+1, j-1 {
		u256[i] = b[j]
	}
	return u256
}

// NBitsToU256 converts compact target (nBits) to a 32-byte U256 little-endian
// representation for the SV2 wire format. Uses NBitsToTarget from adapter.go.
func NBitsToU256(nBits uint32) [32]byte {
	return targetToU256(NBitsToTarget(nBits))
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
