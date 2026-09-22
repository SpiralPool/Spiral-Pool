// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"fmt"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/vardiff"
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

// Channel represents a standard or extended mining channel within a session
type Channel struct {
	ID              uint32
	UserIdentity    string // wallet.worker
	MinerAddress    string // payout address, parsed from UserIdentity
	WorkerName      string
	NominalHashRate float32
	Difficulty      float64  // share difficulty for new jobs, in V1 stratum units (jobsMu once the channel is added)
	Target          [32]byte // share target (U256, little-endian) (jobsMu once the channel is added)

	// MaxTarget is the easiest target the client accepts; nil means no limit.
	MaxTarget *big.Int

	// ExtranoncePrefix is the pool's part of the coinbase's 12 reserved extranonce
	// bytes, extranonce1 (4) || extranonce2 (8). A standard channel's miner cannot
	// change any of it, so the prefix fills all 12 bytes and fixes the channel's
	// merkle root. An extended channel's prefix is the 4 bytes of extranonce1 and
	// its miner rolls the ExtranonceSize bytes after it.
	ExtranoncePrefix []byte
	Extended         bool
	ExtranonceSize   int

	vardiff *vardiff.SessionState // nil when vardiff is off

	// Jobs sent to this channel: SV2 job ID → pool job
	jobsMu    sync.Mutex
	jobs      map[uint32]channelJob
	jobOrder  []uint32
	nextJobID uint32
	prevHash  string // pool prevhash of the last job sent

	// Stats
	SharesAccepted  atomic.Uint64
	SharesRejected  atomic.Uint64
	LastShareTime   atomic.Int64
	LastSequenceNum atomic.Uint32
}

// Session represents a Stratum V2 client session
type Session struct {
	ID          string
	NumericID   uint64 // share validator session ID; the high bit marks V2
	Conn        *NoiseConn
	RemoteAddr  net.Addr
	State       SessionState
	ConnectedAt time.Time

	// Protocol negotiation
	ProtocolVersion uint16
	Flags           uint32 // the client's SetupConnection requirements
	VendorID        string
	DeviceID        string

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

// validTransitions defines the allowed state machine transitions for Stratum V2 sessions.
// A client must progress through the protocol in order: handshake → setup → channel → mining.
// StateDisconnected is always reachable from any state (e.g., error path, explicit close).
// StateMining allows re-opening channels (multiple channels per session is valid).
var validTransitions = map[SessionState]map[SessionState]bool{
	StateConnected:         {StateHandshakeComplete: true, StateDisconnected: true},
	StateHandshakeComplete: {StateSetupComplete: true, StateDisconnected: true},
	StateSetupComplete:     {StateChannelOpen: true, StateDisconnected: true},
	StateChannelOpen:       {StateMining: true, StateDisconnected: true},
	StateMining:            {StateChannelOpen: true, StateDisconnected: true}, // re-open channel is valid
	StateDisconnected:      {}, // terminal state
}

// TransitionTo atomically validates and applies a state transition.
// Returns an error if the transition is not permitted by the protocol state machine.
// SECURITY (F-04): Prevents clients from skipping required handshake/setup phases
// and eliminates the "safe by accident" reliance on per-handler state checks alone.
func (s *Session) TransitionTo(next SessionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if allowed := validTransitions[s.State]; !allowed[next] {
		return fmt.Errorf("invalid V2 state transition %s → %s", s.State, next)
	}
	s.State = next
	return nil
}

// SetState updates the session state without transition validation.
// Prefer TransitionTo for all protocol-driven state changes.
// SetState is retained for internal use (e.g., forced disconnect on error paths).
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

// AddChannel creates a new standard mining channel. extranoncePrefix must be the
// 12-byte coinbase extranonce the channel mines with.
func (s *Session) AddChannel(userIdentity string, hashRate float32, difficulty float64, target [32]byte, extranoncePrefix []byte) *Channel {
	return s.addChannel(newChannel(userIdentity, hashRate, difficulty, target, extranoncePrefix))
}

// newChannel builds a channel that is not yet part of a session, so every field can
// be set before job broadcasts can see it.
func newChannel(userIdentity string, hashRate float32, difficulty float64, target [32]byte, extranoncePrefix []byte) *Channel {
	address, worker := splitUserIdentity(userIdentity)
	return &Channel{
		UserIdentity:     userIdentity,
		MinerAddress:     address,
		WorkerName:       worker,
		NominalHashRate:  hashRate,
		Difficulty:       difficulty,
		Target:           target,
		ExtranoncePrefix: extranoncePrefix,
		jobs:             make(map[uint32]channelJob),
	}
}

// addChannel assigns the channel an ID and adds it to the session.
func (s *Session) addChannel(ch *Channel) *Channel {
	s.channelsMu.Lock()
	defer s.channelsMu.Unlock()

	ch.ID = s.nextChanID.Add(1) - 1
	s.channels[ch.ID] = ch
	return ch
}

// splitUserIdentity splits "address.worker" at the last dot, as V1 does for
// usernames. An identity with no dot is all address, with worker "default".
func splitUserIdentity(identity string) (address, worker string) {
	for i := len(identity) - 1; i >= 0; i-- {
		if identity[i] == '.' {
			return identity[:i], identity[i+1:]
		}
	}
	return identity, "default"
}

// channelJob is a pool job as sent to one channel.
type channelJob struct {
	poolJobID      string
	versionRolling bool
	versionMask    uint32
	difficulty     float64 // the channel's share difficulty when the job was sent
}

// addJob records a job sent to the channel and returns its SV2 job ID, evicting
// the oldest beyond maxChannelJobs. The caller must hold jobsMu.
func (ch *Channel) addJob(job channelJob) uint32 {
	ch.nextJobID++
	id := ch.nextJobID
	ch.jobs[id] = job
	ch.jobOrder = append(ch.jobOrder, id)
	for len(ch.jobOrder) > maxChannelJobs {
		delete(ch.jobs, ch.jobOrder[0])
		ch.jobOrder = ch.jobOrder[1:]
	}
	return id
}

// lookupJob returns the pool job for an SV2 job ID sent to this channel.
func (ch *Channel) lookupJob(id uint32) (channelJob, bool) {
	ch.jobsMu.Lock()
	defer ch.jobsMu.Unlock()
	job, ok := ch.jobs[id]
	return job, ok
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
