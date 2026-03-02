// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// SessionState represents the state of a V2 session
type SessionState int

const (
	StateConnected SessionState = iota
	StateHandshakeComplete
	StateSetupComplete
	StateChannelOpen
	StateMining
	StateDisconnected
)

func (s SessionState) String() string {
	switch s {
	case StateConnected:
		return "connected"
	case StateHandshakeComplete:
		return "handshake_complete"
	case StateSetupComplete:
		return "setup_complete"
	case StateChannelOpen:
		return "channel_open"
	case StateMining:
		return "mining"
	case StateDisconnected:
		return "disconnected"
	default:
		return "unknown"
	}
}

// Channel represents a mining channel within a session
type Channel struct {
	ID              uint32
	UserIdentity    string // wallet.worker
	NominalHashRate float32
	TargetNBits     uint32 // Current target in compact form
	ExtraNonce2Size uint16
	ExtraNonce2     []byte // Channel's extranonce2 prefix

	// Stats
	SharesAccepted  atomic.Uint64
	SharesRejected  atomic.Uint64
	LastShareTime   atomic.Int64
	LastSequenceNum atomic.Uint32
}

// Session represents a Stratum V2 client session
type Session struct {
	ID          string
	Conn        *NoiseConn
	RemoteAddr  net.Addr
	State       SessionState
	ConnectedAt time.Time

	// Protocol negotiation
	ProtocolVersion uint16
	Flags           uint32
	VendorID        string

	// Channels (a session can have multiple mining channels)
	channels   map[uint32]*Channel
	channelsMu sync.RWMutex
	nextChanID atomic.Uint32

	// Job tracking
	CurrentJobID  atomic.Uint32
	LastJobSentAt atomic.Int64

	// Session-level stats
	TotalSharesAccepted atomic.Uint64
	TotalSharesRejected atomic.Uint64
	BytesSent           atomic.Uint64
	BytesReceived       atomic.Uint64

	// Synchronization
	mu        sync.RWMutex
	sendMu    sync.Mutex
	closeCh   chan struct{}
	closeOnce sync.Once
}

// NewSession creates a new V2 session
func NewSession(id string, conn *NoiseConn) *Session {
	s := &Session{
		ID:          id,
		Conn:        conn,
		RemoteAddr:  conn.RemoteAddr(),
		State:       StateHandshakeComplete,
		ConnectedAt: time.Now(),
		channels:    make(map[uint32]*Channel),
		closeCh:     make(chan struct{}),
	}
	s.nextChanID.Store(1) // Start channel IDs at 1
	return s
}

// SetState updates the session state
func (s *Session) SetState(state SessionState) {
	s.mu.Lock()
	s.State = state
	s.mu.Unlock()
}

// GetState returns the current session state
func (s *Session) GetState() SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.State
}

// AddChannel creates a new mining channel
func (s *Session) AddChannel(userIdentity string, hashRate float32, targetNBits uint32, extraNonce2Size uint16) *Channel {
	s.channelsMu.Lock()
	defer s.channelsMu.Unlock()

	chanID := s.nextChanID.Add(1) - 1
	ch := &Channel{
		ID:              chanID,
		UserIdentity:    userIdentity,
		NominalHashRate: hashRate,
		TargetNBits:     targetNBits,
		ExtraNonce2Size: extraNonce2Size,
		ExtraNonce2:     make([]byte, extraNonce2Size),
	}

	// Generate unique extranonce2 for this channel
	// Use channel ID as prefix
	if extraNonce2Size >= 4 {
		ch.ExtraNonce2[0] = byte(chanID)
		ch.ExtraNonce2[1] = byte(chanID >> 8)
		ch.ExtraNonce2[2] = byte(chanID >> 16)
		ch.ExtraNonce2[3] = byte(chanID >> 24)
	}

	s.channels[chanID] = ch
	return ch
}

// GetChannel returns a channel by ID
func (s *Session) GetChannel(id uint32) *Channel {
	s.channelsMu.RLock()
	defer s.channelsMu.RUnlock()
	return s.channels[id]
}

// GetChannels returns all channels
func (s *Session) GetChannels() []*Channel {
	s.channelsMu.RLock()
	defer s.channelsMu.RUnlock()

	channels := make([]*Channel, 0, len(s.channels))
	for _, ch := range s.channels {
		channels = append(channels, ch)
	}
	return channels
}

// RemoveChannel removes a channel
func (s *Session) RemoveChannel(id uint32) {
	s.channelsMu.Lock()
	delete(s.channels, id)
	s.channelsMu.Unlock()
}

// ChannelCount returns the number of active channels
func (s *Session) ChannelCount() int {
	s.channelsMu.RLock()
	defer s.channelsMu.RUnlock()
	return len(s.channels)
}

// Send sends an encrypted message with write deadline protection.
// FIX 2e: Set write deadline before write, clear after — prevents slow-read
// clients from blocking the send path and holding the sendMu lock indefinitely.
func (s *Session) Send(data []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	_ = s.Conn.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) // #nosec G104
	n, err := s.Conn.Write(data)
	_ = s.Conn.conn.SetWriteDeadline(time.Time{}) // #nosec G104 — clear deadline
	if err != nil {
		return err
	}
	s.BytesSent.Add(uint64(n))
	return nil
}

// Close closes the session
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.closeCh)
		s.SetState(StateDisconnected)
		err = s.Conn.Close()
	})
	return err
}

// IsClosed returns true if the session is closed
func (s *Session) IsClosed() bool {
	select {
	case <-s.closeCh:
		return true
	default:
		return false
	}
}

// Duration returns how long the session has been connected
func (s *Session) Duration() time.Duration {
	return time.Since(s.ConnectedAt)
}

// String returns a string representation
func (s *Session) String() string {
	return fmt.Sprintf("Session{id=%s, state=%s, channels=%d, addr=%s}",
		s.ID, s.GetState(), s.ChannelCount(), s.RemoteAddr)
}

// SessionManager manages V2 sessions
type SessionManager struct {
	sessions    sync.Map // map[string]*Session
	count       atomic.Int64
	broadcastWg sync.WaitGroup // CRITICAL FIX: Track broadcast goroutines for clean shutdown
}

// NewSessionManager creates a new session manager
func NewSessionManager() *SessionManager {
	return &SessionManager{}
}

// Add adds a session
func (sm *SessionManager) Add(session *Session) {
	sm.sessions.Store(session.ID, session)
	sm.count.Add(1)
}

// Get returns a session by ID
func (sm *SessionManager) Get(id string) *Session {
	if v, ok := sm.sessions.Load(id); ok {
		return v.(*Session)
	}
	return nil
}

// Remove removes a session
func (sm *SessionManager) Remove(id string) {
	if _, ok := sm.sessions.LoadAndDelete(id); ok {
		sm.count.Add(-1)
	}
}

// Count returns the number of active sessions
func (sm *SessionManager) Count() int64 {
	return sm.count.Load()
}

// ForEach iterates over all sessions
func (sm *SessionManager) ForEach(fn func(*Session) bool) {
	sm.sessions.Range(func(key, value interface{}) bool {
		return fn(value.(*Session))
	})
}

// Broadcast sends a message to all sessions
func (sm *SessionManager) Broadcast(data []byte) {
	sm.sessions.Range(func(key, value interface{}) bool {
		session := value.(*Session)
		if session.GetState() >= StateMining {
			// CRITICAL FIX: Track goroutine in WaitGroup for clean shutdown
			sm.broadcastWg.Add(1)
			go func(s *Session) {
				defer sm.broadcastWg.Done()
				s.Send(data)
			}(session)
		}
		return true
	})
}

// BroadcastToChannel sends a message to all sessions with a specific channel
func (sm *SessionManager) BroadcastToChannel(channelID uint32, data []byte) {
	sm.sessions.Range(func(key, value interface{}) bool {
		session := value.(*Session)
		if ch := session.GetChannel(channelID); ch != nil {
			// CRITICAL FIX: Track goroutine in WaitGroup for clean shutdown
			sm.broadcastWg.Add(1)
			go func(s *Session) {
				defer sm.broadcastWg.Done()
				s.Send(data)
			}(session)
		}
		return true
	})
}

// CloseAll closes all sessions
func (sm *SessionManager) CloseAll() {
	// CRITICAL FIX: Wait for any pending broadcasts to complete before closing sessions
	sm.broadcastWg.Wait()

	sm.sessions.Range(func(key, value interface{}) bool {
		session := value.(*Session)
		_ = session.Close() // #nosec G104 - error ignored during shutdown
		return true
	})
}

// Stats returns aggregate statistics
type SessionStats struct {
	ActiveSessions int64
	TotalChannels  int
	TotalAccepted  uint64
	TotalRejected  uint64
	TotalBytesSent uint64
	TotalBytesRecv uint64
}

func (sm *SessionManager) Stats() SessionStats {
	stats := SessionStats{
		ActiveSessions: sm.count.Load(),
	}

	sm.sessions.Range(func(key, value interface{}) bool {
		session := value.(*Session)
		stats.TotalChannels += session.ChannelCount()
		stats.TotalAccepted += session.TotalSharesAccepted.Load()
		stats.TotalRejected += session.TotalSharesRejected.Load()
		stats.TotalBytesSent += session.BytesSent.Load()
		stats.TotalBytesRecv += session.BytesReceived.Load()
		return true
	})

	return stats
}
