// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v2 provides tests for session and channel management.
//
// These tests validate:
// - Session lifecycle and state transitions
// - Channel creation and management
// - Session manager operations
// - Concurrent access safety
package v2

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockNoiseConn creates a mock NoiseConn for testing
func mockNoiseConn() *NoiseConn {
	serverConn, _ := net.Pipe()
	return &NoiseConn{conn: serverConn}
}

// TestNewSession validates session creation.
func TestNewSession(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session-1", nc)

	if session.ID != "test-session-1" {
		t.Errorf("ID = %s, want test-session-1", session.ID)
	}
	if session.Conn != nc {
		t.Error("Conn not set correctly")
	}
	// Session is created after Noise handshake completes
	if session.GetState() != StateHandshakeComplete {
		t.Errorf("initial state = %d, want %d", session.GetState(), StateHandshakeComplete)
	}
	if session.ConnectedAt.IsZero() {
		t.Error("ConnectedAt should be set")
	}
}

// TestSessionStateTransitions validates state changes.
func TestSessionStateTransitions(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session", nc)

	states := []SessionState{
		StateHandshakeComplete,
		StateSetupComplete,
		StateChannelOpen,
		StateMining,
		StateDisconnected,
	}

	for _, state := range states {
		session.SetState(state)
		if session.GetState() != state {
			t.Errorf("state = %d, want %d", session.GetState(), state)
		}
	}
}

// TestSessionAddChannel validates channel creation.
func TestSessionAddChannel(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session", nc)

	// Add first channel
	ch1 := session.AddChannel("worker1", 1000000.0, 0x1d00ffff, 8)

	if ch1 == nil {
		t.Fatal("AddChannel returned nil")
	}
	if ch1.ID != 1 {
		t.Errorf("channel ID = %d, want 1", ch1.ID)
	}
	if ch1.UserIdentity != "worker1" {
		t.Errorf("UserIdentity = %s, want worker1", ch1.UserIdentity)
	}
	if ch1.NominalHashRate != 1000000.0 {
		t.Errorf("NominalHashRate = %f, want 1000000.0", ch1.NominalHashRate)
	}
	if ch1.TargetNBits != 0x1d00ffff {
		t.Errorf("TargetNBits = %x, want 1d00ffff", ch1.TargetNBits)
	}
	if ch1.ExtraNonce2Size != 8 {
		t.Errorf("ExtraNonce2Size = %d, want 8", ch1.ExtraNonce2Size)
	}
	if len(ch1.ExtraNonce2) != 8 {
		t.Errorf("ExtraNonce2 length = %d, want 8", len(ch1.ExtraNonce2))
	}

	// Add second channel
	ch2 := session.AddChannel("worker2", 2000000.0, 0x1d00ffff, 8)
	if ch2.ID != 2 {
		t.Errorf("second channel ID = %d, want 2", ch2.ID)
	}

	// Verify channel count
	if session.ChannelCount() != 2 {
		t.Errorf("ChannelCount = %d, want 2", session.ChannelCount())
	}
}

// TestSessionGetChannel validates channel retrieval.
func TestSessionGetChannel(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session", nc)
	ch := session.AddChannel("worker1", 1000000.0, 0x1d00ffff, 8)

	// Get existing channel
	retrieved := session.GetChannel(ch.ID)
	if retrieved == nil {
		t.Fatal("GetChannel returned nil for existing channel")
	}
	if retrieved.ID != ch.ID {
		t.Errorf("retrieved channel ID = %d, want %d", retrieved.ID, ch.ID)
	}

	// Get non-existent channel
	nonExistent := session.GetChannel(999)
	if nonExistent != nil {
		t.Error("GetChannel should return nil for non-existent channel")
	}
}

// TestSessionRemoveChannel validates channel removal.
func TestSessionRemoveChannel(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session", nc)
	ch := session.AddChannel("worker1", 1000000.0, 0x1d00ffff, 8)

	// Remove channel
	session.RemoveChannel(ch.ID)

	// Verify removal
	if session.GetChannel(ch.ID) != nil {
		t.Error("channel should be removed")
	}
	if session.ChannelCount() != 0 {
		t.Errorf("ChannelCount = %d, want 0", session.ChannelCount())
	}
}

// TestSessionGetChannels validates getting all channels.
func TestSessionGetChannels(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session", nc)
	session.AddChannel("worker1", 1000000.0, 0x1d00ffff, 8)
	session.AddChannel("worker2", 2000000.0, 0x1d00ffff, 8)
	session.AddChannel("worker3", 3000000.0, 0x1d00ffff, 8)

	channels := session.GetChannels()
	if len(channels) != 3 {
		t.Errorf("GetChannels length = %d, want 3", len(channels))
	}
}

// TestSessionDuration validates duration calculation.
func TestSessionDuration(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session", nc)

	// Wait a bit
	time.Sleep(50 * time.Millisecond)

	duration := session.Duration()
	if duration < 50*time.Millisecond {
		t.Errorf("Duration = %v, expected >= 50ms", duration)
	}
}

// TestSessionClose validates session closing.
func TestSessionClose(t *testing.T) {
	nc := mockNoiseConn()
	session := NewSession("test-session", nc)

	if session.IsClosed() {
		t.Error("new session should not be closed")
	}

	session.Close()

	if !session.IsClosed() {
		t.Error("session should be closed after Close()")
	}
	if session.GetState() != StateDisconnected {
		t.Errorf("state = %d, want %d", session.GetState(), StateDisconnected)
	}
}

// TestSessionSend validates that Send method exists and updates counters on success.
// Note: Full encryption and send/receive testing is done in noise_test.go.
func TestSessionSend(t *testing.T) {
	serverConn, clientConn := net.Pipe()

	// Generate server keys for handshake
	serverKeys, err := GenerateServerKeys()
	if err != nil {
		t.Fatalf("Failed to generate server keys: %v", err)
	}

	// Perform handshake with timeouts
	type handshakeResult struct {
		nc  *NoiseConn
		err error
	}

	serverCh := make(chan handshakeResult, 1)
	clientCh := make(chan handshakeResult, 1)

	go func() {
		nc, err := ServerHandshake(serverConn, serverKeys)
		serverCh <- handshakeResult{nc, err}
	}()

	go func() {
		nc, _, err := ClientHandshake(clientConn)
		clientCh <- handshakeResult{nc, err}
	}()

	// Wait for both handshakes with timeout
	timeout := time.After(5 * time.Second)

	var serverNC, clientNC *NoiseConn
	for i := 0; i < 2; i++ {
		select {
		case res := <-serverCh:
			if res.err != nil {
				serverConn.Close()
				clientConn.Close()
				t.Fatalf("Server handshake failed: %v", res.err)
			}
			serverNC = res.nc
		case res := <-clientCh:
			if res.err != nil {
				serverConn.Close()
				clientConn.Close()
				t.Fatalf("Client handshake failed: %v", res.err)
			}
			clientNC = res.nc
		case <-timeout:
			serverConn.Close()
			clientConn.Close()
			t.Fatal("Handshake timeout")
		}
	}
	defer serverNC.Close()
	defer clientNC.Close()

	// Create session with properly established Noise connection
	session := NewSession("test-session", serverNC)

	// Build a valid V2 message
	testData := EncodeMessage(MsgSetupConnectionSuccess, []byte{0x01, 0x02, 0x03})

	// Start a reader goroutine to consume the data (prevents Write from blocking)
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		_, err := clientNC.Read(buf)
		readDone <- err
	}()

	// Send should now work
	err = session.Send(testData)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Wait for read to complete
	select {
	case readErr := <-readDone:
		if readErr != nil {
			t.Logf("Read error (may be expected): %v", readErr)
		}
	case <-time.After(1 * time.Second):
		t.Log("Read timed out - data may not have been received")
	}

	// Verify bytes sent counter was updated
	if session.BytesSent.Load() == 0 {
		t.Error("BytesSent should be updated after successful send")
	}
}

// TestSessionShareCounters validates share counting.
func TestSessionShareCounters(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session", nc)

	// Simulate share acceptance/rejection
	session.TotalSharesAccepted.Add(10)
	session.TotalSharesRejected.Add(2)

	if session.TotalSharesAccepted.Load() != 10 {
		t.Errorf("TotalSharesAccepted = %d, want 10", session.TotalSharesAccepted.Load())
	}
	if session.TotalSharesRejected.Load() != 2 {
		t.Errorf("TotalSharesRejected = %d, want 2", session.TotalSharesRejected.Load())
	}
}

// TestChannelShareCounters validates channel-level share counting.
func TestChannelShareCounters(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session", nc)
	ch := session.AddChannel("worker1", 1000000.0, 0x1d00ffff, 8)

	ch.SharesAccepted.Add(5)
	ch.SharesRejected.Add(1)

	if ch.SharesAccepted.Load() != 5 {
		t.Errorf("SharesAccepted = %d, want 5", ch.SharesAccepted.Load())
	}
	if ch.SharesRejected.Load() != 1 {
		t.Errorf("SharesRejected = %d, want 1", ch.SharesRejected.Load())
	}
}

// TestSessionManagerAdd validates adding sessions.
func TestSessionManagerAdd(t *testing.T) {
	sm := NewSessionManager()

	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("session-1", nc)
	sm.Add(session)

	if sm.Count() != 1 {
		t.Errorf("Count = %d, want 1", sm.Count())
	}

	// Add another
	nc2 := mockNoiseConn()
	defer nc2.Close()
	session2 := NewSession("session-2", nc2)
	sm.Add(session2)

	if sm.Count() != 2 {
		t.Errorf("Count = %d, want 2", sm.Count())
	}
}

// TestSessionManagerGet validates getting sessions.
func TestSessionManagerGet(t *testing.T) {
	sm := NewSessionManager()

	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("session-1", nc)
	sm.Add(session)

	// Get existing session
	retrieved := sm.Get("session-1")
	if retrieved == nil {
		t.Fatal("Get returned nil for existing session")
	}
	if retrieved.ID != "session-1" {
		t.Errorf("retrieved ID = %s, want session-1", retrieved.ID)
	}

	// Get non-existent session
	nonExistent := sm.Get("non-existent")
	if nonExistent != nil {
		t.Error("Get should return nil for non-existent session")
	}
}

// TestSessionManagerRemove validates removing sessions.
func TestSessionManagerRemove(t *testing.T) {
	sm := NewSessionManager()

	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("session-1", nc)
	sm.Add(session)

	sm.Remove("session-1")

	if sm.Count() != 0 {
		t.Errorf("Count = %d, want 0", sm.Count())
	}
	if sm.Get("session-1") != nil {
		t.Error("session should be removed")
	}
}

// TestSessionManagerForEach validates iterating sessions.
func TestSessionManagerForEach(t *testing.T) {
	sm := NewSessionManager()

	// Add multiple sessions
	for i := 0; i < 5; i++ {
		nc := mockNoiseConn()
		defer nc.Close()
		session := NewSession(string(rune('A'+i)), nc)
		sm.Add(session)
	}

	count := 0
	sm.ForEach(func(s *Session) bool {
		count++
		return true
	})

	if count != 5 {
		t.Errorf("ForEach visited %d sessions, want 5", count)
	}

	// Test early exit
	count = 0
	sm.ForEach(func(s *Session) bool {
		count++
		return count < 3 // Stop after 3
	})

	if count != 3 {
		t.Errorf("ForEach with early exit visited %d sessions, want 3", count)
	}
}

// TestSessionManagerCloseAll validates closing all sessions.
func TestSessionManagerCloseAll(t *testing.T) {
	sm := NewSessionManager()

	sessions := make([]*Session, 5)
	for i := 0; i < 5; i++ {
		nc := mockNoiseConn()
		sessions[i] = NewSession(string(rune('A'+i)), nc)
		sm.Add(sessions[i])
	}

	sm.CloseAll()

	// All sessions should be closed
	for _, s := range sessions {
		if !s.IsClosed() {
			t.Errorf("session %s should be closed", s.ID)
		}
	}

	// Note: CloseAll closes sessions but keeps them in the manager
	// (useful for statistics/cleanup). Sessions are removed via Remove().
	if sm.Count() != 5 {
		t.Errorf("Count = %d, want 5 (CloseAll doesn't remove)", sm.Count())
	}
}

// TestSessionManagerStats validates statistics gathering.
func TestSessionManagerStats(t *testing.T) {
	sm := NewSessionManager()

	// Add sessions with channels and shares
	for i := 0; i < 3; i++ {
		nc := mockNoiseConn()
		defer nc.Close()
		session := NewSession(string(rune('A'+i)), nc)
		session.AddChannel("worker", 1000000.0, 0x1d00ffff, 8)
		session.TotalSharesAccepted.Add(10)
		session.TotalSharesRejected.Add(2)
		session.BytesSent.Add(1000)
		session.BytesReceived.Add(500)
		sm.Add(session)
	}

	stats := sm.Stats()

	if stats.ActiveSessions != 3 {
		t.Errorf("ActiveSessions = %d, want 3", stats.ActiveSessions)
	}
	if stats.TotalChannels != 3 {
		t.Errorf("TotalChannels = %d, want 3", stats.TotalChannels)
	}
	if stats.TotalAccepted != 30 {
		t.Errorf("TotalAccepted = %d, want 30", stats.TotalAccepted)
	}
	if stats.TotalRejected != 6 {
		t.Errorf("TotalRejected = %d, want 6", stats.TotalRejected)
	}
	if stats.TotalBytesSent != 3000 {
		t.Errorf("TotalBytesSent = %d, want 3000", stats.TotalBytesSent)
	}
	if stats.TotalBytesRecv != 1500 {
		t.Errorf("TotalBytesRecv = %d, want 1500", stats.TotalBytesRecv)
	}
}

// TestSessionConcurrentAccess validates thread safety.
func TestSessionConcurrentAccess(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()

	session := NewSession("test-session", nc)

	var wg sync.WaitGroup

	// Concurrent channel operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ch := session.AddChannel("worker", 1000000.0, 0x1d00ffff, 8)
			session.GetChannel(ch.ID)
			session.GetChannels()
			session.ChannelCount()
		}(i)
	}

	// Concurrent state changes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.SetState(StateMining)
			session.GetState()
		}()
	}

	// Concurrent counter updates
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.TotalSharesAccepted.Add(1)
			session.TotalSharesRejected.Add(1)
			session.BytesSent.Add(100)
			session.BytesReceived.Add(50)
		}()
	}

	wg.Wait()

	// Basic sanity checks
	if session.ChannelCount() != 10 {
		t.Errorf("ChannelCount = %d, want 10", session.ChannelCount())
	}
	if session.TotalSharesAccepted.Load() != 10 {
		t.Errorf("TotalSharesAccepted = %d, want 10", session.TotalSharesAccepted.Load())
	}
}

// TestSessionManagerConcurrentAccess validates manager thread safety.
func TestSessionManagerConcurrentAccess(t *testing.T) {
	sm := NewSessionManager()

	var wg sync.WaitGroup
	var counter atomic.Uint32

	// Concurrent adds
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := counter.Add(1)
			nc := mockNoiseConn()
			defer nc.Close()
			session := NewSession(string(rune('A'+id%26))+string(rune('0'+id/26)), nc)
			sm.Add(session)
		}()
	}

	// Concurrent reads while adding
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sm.Count()
			sm.Stats()
			sm.ForEach(func(s *Session) bool {
				return true
			})
		}()
	}

	wg.Wait()

	// Should have some sessions (exact count depends on timing)
	if sm.Count() == 0 {
		t.Error("should have some sessions after concurrent adds")
	}
}

// Benchmark tests

func BenchmarkSessionAddChannel(b *testing.B) {
	nc := mockNoiseConn()
	defer nc.Close()
	session := NewSession("test-session", nc)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session.AddChannel("worker", 1000000.0, 0x1d00ffff, 8)
	}
}

func BenchmarkSessionGetChannel(b *testing.B) {
	nc := mockNoiseConn()
	defer nc.Close()
	session := NewSession("test-session", nc)
	ch := session.AddChannel("worker", 1000000.0, 0x1d00ffff, 8)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session.GetChannel(ch.ID)
	}
}

func BenchmarkSessionManagerAdd(b *testing.B) {
	sm := NewSessionManager()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nc := mockNoiseConn()
		session := NewSession(string(rune(i)), nc)
		sm.Add(session)
	}
}

func BenchmarkSessionManagerGet(b *testing.B) {
	sm := NewSessionManager()
	nc := mockNoiseConn()
	defer nc.Close()
	session := NewSession("test-session", nc)
	sm.Add(session)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Get("test-session")
	}
}
