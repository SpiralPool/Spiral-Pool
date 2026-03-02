// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v2 provides tests for Stratum V2 server.
//
// These tests validate:
// - Server configuration and startup
// - Connection handling
// - Message processing
// - Share submission flow
// - Job broadcasting
package v2

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// mockJobProvider implements JobProvider for testing
type mockJobProvider struct {
	currentJob *MiningJobData
	jobs       map[uint32]*MiningJobData
	callback   func()
}

func newMockJobProvider() *mockJobProvider {
	var merkleRoot [32]byte
	var prevHash [32]byte
	for i := range merkleRoot {
		merkleRoot[i] = byte(i)
		prevHash[i] = byte(i * 2)
	}

	job := &MiningJobData{
		ID:         1,
		PrevHash:   prevHash,
		MerkleRoot: merkleRoot,
		Version:    0x20000000,
		NBits:      0x1d00ffff,
		NTime:      1609459200,
		CleanJobs:  true,
	}

	return &mockJobProvider{
		currentJob: job,
		jobs:       map[uint32]*MiningJobData{1: job},
	}
}

func (m *mockJobProvider) GetCurrentJob() *MiningJobData {
	return m.currentJob
}

func (m *mockJobProvider) GetJob(id uint32) *MiningJobData {
	return m.jobs[id]
}

func (m *mockJobProvider) RegisterNewBlockCallback(fn func()) {
	m.callback = fn
}

// mockShareHandler implements ShareHandler for testing
type mockShareHandler struct {
	shares     []*ShareSubmission
	acceptAll  bool
	blockCount atomic.Uint32
}

func newMockShareHandler(acceptAll bool) *mockShareHandler {
	return &mockShareHandler{
		shares:    make([]*ShareSubmission, 0),
		acceptAll: acceptAll,
	}
}

func (m *mockShareHandler) ProcessShare(share *ShareSubmission) *ShareResult {
	m.shares = append(m.shares, share)
	return &ShareResult{
		Accepted: m.acceptAll,
		IsBlock:  false,
	}
}

// TestDefaultServerConfig validates default configuration.
func TestDefaultServerConfig(t *testing.T) {
	cfg := DefaultServerConfig()

	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.ProtocolVersion != 2 {
		t.Errorf("ProtocolVersion = %d, want 2", cfg.ProtocolVersion)
	}
	if cfg.MaxConnections != 100000 {
		t.Errorf("MaxConnections = %d, want 100000", cfg.MaxConnections)
	}
	if cfg.MaxChannelsPerSession != 10 {
		t.Errorf("MaxChannelsPerSession = %d, want 10", cfg.MaxChannelsPerSession)
	}
	if cfg.ReadTimeout != 5*time.Minute {
		t.Errorf("ReadTimeout = %v, want 5m", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 30*time.Second {
		t.Errorf("WriteTimeout = %v, want 30s", cfg.WriteTimeout)
	}
	if cfg.DefaultTargetNBits != 0x1d00ffff {
		t.Errorf("DefaultTargetNBits = %x, want 1d00ffff", cfg.DefaultTargetNBits)
	}
	if cfg.ExtraNonce2Size != 8 {
		t.Errorf("ExtraNonce2Size = %d, want 8", cfg.ExtraNonce2Size)
	}
}

// TestNewServer validates server creation.
func TestNewServer(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	server, err := NewServer(nil, logger.Sugar())
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if server.config == nil {
		t.Error("config should be set to defaults")
	}
	if server.serverKeys == nil {
		t.Error("server keys should be generated")
	}
	if server.sessions == nil {
		t.Error("session manager should be initialized")
	}

	// Verify public key is not empty
	allZero := true
	for _, b := range server.PublicKey() {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("public key should not be all zeros")
	}
}

// TestNewServerWithConfig validates custom configuration.
func TestNewServerWithConfig(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := &ServerConfig{
		ListenAddr:     "127.0.0.1",
		Port:           3335,
		MaxConnections: 500,
	}

	server, err := NewServer(cfg, logger.Sugar())
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if server.config.Port != 3335 {
		t.Errorf("Port = %d, want 3335", server.config.Port)
	}
	if server.config.MaxConnections != 500 {
		t.Errorf("MaxConnections = %d, want 500", server.config.MaxConnections)
	}
}

// TestServerSetJobProvider validates job provider setup.
func TestServerSetJobProvider(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	server, _ := NewServer(nil, logger.Sugar())

	jp := newMockJobProvider()
	server.SetJobProvider(jp)

	if server.jobProvider == nil {
		t.Error("job provider should be set")
	}
	if jp.callback == nil {
		t.Error("new block callback should be registered")
	}
}

// TestServerSetShareHandler validates share handler setup.
func TestServerSetShareHandler(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	server, _ := NewServer(nil, logger.Sugar())

	sh := newMockShareHandler(true)
	server.SetShareHandler(sh)

	if server.shareHandler == nil {
		t.Error("share handler should be set")
	}
}

// TestServerStartStop validates server lifecycle.
func TestServerStartStop(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := &ServerConfig{
		ListenAddr: "127.0.0.1",
		Port:       0, // Let OS pick a port
	}

	server, err := NewServer(cfg, logger.Sugar())
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !server.running.Load() {
		t.Error("server should be running")
	}

	err = server.Stop()
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if server.running.Load() {
		t.Error("server should not be running after Stop")
	}
}

// TestServerCallbacks validates connection callbacks.
func TestServerCallbacks(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	server, _ := NewServer(nil, logger.Sugar())

	var connectCalled, disconnectCalled bool
	var connectedSession, disconnectedSession *Session

	server.OnConnect(func(s *Session) {
		connectCalled = true
		connectedSession = s
	})

	server.OnDisconnect(func(s *Session) {
		disconnectCalled = true
		disconnectedSession = s
	})

	// Verify callbacks are set
	if server.onConnect == nil {
		t.Error("onConnect should be set")
	}
	if server.onDisconnect == nil {
		t.Error("onDisconnect should be set")
	}

	// Note: Actual callback invocation requires full connection flow
	_ = connectCalled
	_ = disconnectCalled
	_ = connectedSession
	_ = disconnectedSession
}

// TestServerStats validates statistics gathering.
func TestServerStats(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	server, _ := NewServer(nil, logger.Sugar())

	stats := server.Stats()

	if _, ok := stats["active_sessions"]; !ok {
		t.Error("stats should include active_sessions")
	}
	if _, ok := stats["total_connections"]; !ok {
		t.Error("stats should include total_connections")
	}
	if _, ok := stats["total_shares"]; !ok {
		t.Error("stats should include total_shares")
	}
	if _, ok := stats["total_blocks"]; !ok {
		t.Error("stats should include total_blocks")
	}
}

// TestServerBroadcastNewBlock validates job broadcasting.
func TestServerBroadcastNewBlock(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	server, _ := NewServer(nil, logger.Sugar())

	jp := newMockJobProvider()
	server.SetJobProvider(jp)

	// Trigger broadcast (with no sessions, should just return)
	server.broadcastNewBlock()

	// With no sessions, the broadcast should complete without error
	// Note: We don't add mock sessions here because mockNoiseConn doesn't
	// have proper cipher states and would panic on Write
}

// TestHandleSetupConnection validates SetupConnection message parsing.
// Note: Full handler tests with Send require proper Noise encryption setup.
func TestHandleSetupConnection(t *testing.T) {
	// Build SetupConnection payload (SV2 spec wire order)
	enc := NewEncoder()
	enc.WriteU8(ProtocolMiningV2)
	enc.WriteU16(2) // MinVersion
	enc.WriteU16(2) // MaxVersion
	enc.WriteU32(ProtocolFlagRequiresStandardJobs)
	enc.WriteB0_255("pool.example.com") // endpoint_host
	enc.WriteU16(3334)                  // endpoint_port
	enc.WriteB0_255("TestVendor")       // vendor
	enc.WriteB0_255("1.0")              // hardware_version
	enc.WriteB0_255("2.0")              // firmware
	payload := enc.Bytes()

	// Verify decoding works correctly
	msg, err := DecodeSetupConnection(payload)
	if err != nil {
		t.Fatalf("DecodeSetupConnection failed: %v", err)
	}

	if msg.Protocol != ProtocolMiningV2 {
		t.Errorf("Protocol = %d, want %d", msg.Protocol, ProtocolMiningV2)
	}
	if msg.MinVersion != 2 {
		t.Errorf("MinVersion = %d, want 2", msg.MinVersion)
	}
	if msg.MaxVersion != 2 {
		t.Errorf("MaxVersion = %d, want 2", msg.MaxVersion)
	}
	if msg.VendorID != "TestVendor" {
		t.Errorf("VendorID = %s, want TestVendor", msg.VendorID)
	}
	if msg.EndpointPort != 3334 {
		t.Errorf("EndpointPort = %d, want 3334", msg.EndpointPort)
	}
}

// TestHandleSetupConnectionVersionMismatch validates version mismatch detection.
// Note: The actual handler would panic on Send without proper Noise setup,
// so we test the version checking logic via the decoder and message parsing.
func TestHandleSetupConnectionVersionMismatch(t *testing.T) {
	// Build SetupConnection with unsupported version (SV2 spec wire order)
	enc := NewEncoder()
	enc.WriteU8(ProtocolMiningV2)
	enc.WriteU16(99) // MinVersion - too high
	enc.WriteU16(99) // MaxVersion - too high
	enc.WriteU32(0)
	enc.WriteB0_255("") // endpoint_host
	enc.WriteU16(0)     // endpoint_port
	enc.WriteB0_255("") // vendor
	enc.WriteB0_255("") // hardware_version
	enc.WriteB0_255("") // firmware
	payload := enc.Bytes()

	msg, err := DecodeSetupConnection(payload)
	if err != nil {
		t.Fatalf("DecodeSetupConnection failed: %v", err)
	}

	// Verify version is properly decoded (even though it's incompatible)
	if msg.MinVersion != 99 || msg.MaxVersion != 99 {
		t.Errorf("Version = %d-%d, want 99-99", msg.MinVersion, msg.MaxVersion)
	}

	// Test version compatibility check
	serverVersion := uint16(2)
	if msg.MinVersion <= serverVersion && serverVersion <= msg.MaxVersion {
		t.Error("Server version 2 should NOT be compatible with client version 99-99")
	}
}

// TestHandleOpenStandardMiningChannel validates channel opening message parsing.
func TestHandleOpenStandardMiningChannel(t *testing.T) {
	// Build channel open request (U256 for MaxTarget)
	expectedTarget := NBitsToU256(0x1d00ffff)
	enc := NewEncoder()
	enc.WriteU32(1) // RequestID
	enc.WriteB0_255("DGBaddress.worker1")
	enc.WriteF32(1000000.0)
	enc.WriteBytes(expectedTarget[:]) // U256 (32 bytes)
	payload := enc.Bytes()

	// Test decoding
	msg, err := DecodeOpenStandardMiningChannel(payload)
	if err != nil {
		t.Fatalf("DecodeOpenStandardMiningChannel failed: %v", err)
	}

	if msg.RequestID != 1 {
		t.Errorf("RequestID = %d, want 1", msg.RequestID)
	}
	if msg.UserIdentity != "DGBaddress.worker1" {
		t.Errorf("UserIdentity = %s, want DGBaddress.worker1", msg.UserIdentity)
	}
	if msg.NominalHashRate != 1000000.0 {
		t.Errorf("NominalHashRate = %f, want 1000000.0", msg.NominalHashRate)
	}
	if msg.MaxTarget != expectedTarget {
		t.Errorf("MaxTarget mismatch")
	}

	// Test that session correctly adds channels
	nc := mockNoiseConn()
	defer nc.Close()
	session := NewSession("test-session", nc)

	ch := session.AddChannel(msg.UserIdentity, msg.NominalHashRate, 0x1d00ffff, 8)
	if ch == nil {
		t.Fatal("AddChannel returned nil")
	}

	if session.ChannelCount() != 1 {
		t.Errorf("ChannelCount = %d, want 1", session.ChannelCount())
	}

	if ch.UserIdentity != "DGBaddress.worker1" {
		t.Errorf("UserIdentity = %s, want DGBaddress.worker1", ch.UserIdentity)
	}
}

// TestHandleSubmitSharesStandard validates share submission message parsing.
func TestHandleSubmitSharesStandard(t *testing.T) {
	// Build share submission
	enc := NewEncoder()
	enc.WriteU32(1)          // ChannelID
	enc.WriteU32(1)          // SequenceNum
	enc.WriteU32(42)         // JobID
	enc.WriteU32(0xDEADBEEF) // Nonce
	enc.WriteU32(1609459200) // NTime
	enc.WriteU32(0x20000000) // Version
	payload := enc.Bytes()

	// Test decoding
	msg, err := DecodeSubmitSharesStandard(payload)
	if err != nil {
		t.Fatalf("DecodeSubmitSharesStandard failed: %v", err)
	}

	if msg.ChannelID != 1 {
		t.Errorf("ChannelID = %d, want 1", msg.ChannelID)
	}
	if msg.SequenceNum != 1 {
		t.Errorf("SequenceNum = %d, want 1", msg.SequenceNum)
	}
	if msg.JobID != 42 {
		t.Errorf("JobID = %d, want 42", msg.JobID)
	}
	if msg.Nonce != 0xDEADBEEF {
		t.Errorf("Nonce = %x, want DEADBEEF", msg.Nonce)
	}
	if msg.NTime != 1609459200 {
		t.Errorf("NTime = %d, want 1609459200", msg.NTime)
	}
	if msg.Version != 0x20000000 {
		t.Errorf("Version = %x, want 20000000", msg.Version)
	}

	// Test share handler processing directly
	sh := newMockShareHandler(true)
	share := &ShareSubmission{
		ChannelID: msg.ChannelID,
		JobID:     msg.JobID,
		Nonce:     msg.Nonce,
		NTime:     msg.NTime,
		Version:   msg.Version,
	}

	result := sh.ProcessShare(share)
	if !result.Accepted {
		t.Error("share should be accepted")
	}
	if len(sh.shares) != 1 {
		t.Errorf("shares processed = %d, want 1", len(sh.shares))
	}
}

// TestHandleSubmitSharesStandardNoChannel validates that GetChannel returns nil for invalid channel.
func TestHandleSubmitSharesStandardNoChannel(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()
	session := NewSession("test-session", nc)
	// Don't add any channels

	// Try to get a non-existent channel
	ch := session.GetChannel(999)
	if ch != nil {
		t.Error("GetChannel should return nil for non-existent channel")
	}

	// Verify channel count is 0
	if session.ChannelCount() != 0 {
		t.Errorf("ChannelCount = %d, want 0", session.ChannelCount())
	}
}

// TestHandleCloseChannel validates channel closing via session methods.
func TestHandleCloseChannel(t *testing.T) {
	nc := mockNoiseConn()
	defer nc.Close()
	session := NewSession("test-session", nc)
	ch := session.AddChannel("worker", 1000000.0, 0x1d00ffff, 8)

	if session.ChannelCount() != 1 {
		t.Errorf("ChannelCount = %d, want 1", session.ChannelCount())
	}

	// Remove the channel directly (what handleCloseChannel does internally)
	session.RemoveChannel(ch.ID)

	if session.ChannelCount() != 0 {
		t.Errorf("ChannelCount = %d, want 0 after removal", session.ChannelCount())
	}

	// Verify channel is gone
	if session.GetChannel(ch.ID) != nil {
		t.Error("GetChannel should return nil for removed channel")
	}
}

// TestHandleUnknownMessage validates unknown message handling.
func TestHandleUnknownMessage(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	server, _ := NewServer(nil, logger.Sugar())

	nc := mockNoiseConn()
	defer nc.Close()
	session := NewSession("test-session", nc)

	err := server.handleMessage(session, 0xFF, []byte{0x00})
	if err != ErrUnknownMessage {
		t.Errorf("expected ErrUnknownMessage, got %v", err)
	}
}

// TestServerGenerateSessionID validates session ID generation.
func TestServerGenerateSessionID(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	server, _ := NewServer(nil, logger.Sugar())

	id1 := server.generateSessionID()
	id2 := server.generateSessionID()

	if id1 == "" {
		t.Error("session ID should not be empty")
	}
	if id1 == id2 {
		t.Error("session IDs should be unique")
	}
	if len(id1) != 16 { // 8 bytes = 16 hex chars
		t.Errorf("session ID length = %d, want 16", len(id1))
	}
}

// TestFullConnectionFlow tests a simulated connection flow.
func TestFullConnectionFlow(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := &ServerConfig{
		ListenAddr:            "127.0.0.1",
		Port:                  0,
		ProtocolVersion:       2,
		MaxConnections:        10,
		MaxChannelsPerSession: 5,
		ReadTimeout:           5 * time.Second,
		WriteTimeout:          5 * time.Second,
		DefaultTargetNBits:    0x1d00ffff,
		ExtraNonce2Size:       8,
	}

	server, err := NewServer(cfg, logger.Sugar())
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	server.SetJobProvider(newMockJobProvider())
	server.SetShareHandler(newMockShareHandler(true))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = server.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer server.Stop()

	// Get the actual address
	addr := server.listener.Addr().String()

	// Connect as client
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// At this point we'd need to do Noise handshake
	// For this test, just verify the connection was accepted
	time.Sleep(100 * time.Millisecond)

	if server.totalConnections.Load() != 1 {
		t.Errorf("totalConnections = %d, want 1", server.totalConnections.Load())
	}
}

// Benchmark tests - focusing on encoding/decoding performance

func BenchmarkDecodeSetupConnection(b *testing.B) {
	enc := NewEncoder()
	enc.WriteU8(ProtocolMiningV2)
	enc.WriteU16(2)
	enc.WriteU16(2)
	enc.WriteU32(ProtocolFlagRequiresStandardJobs)
	enc.WriteB0_255("pool.example.com")
	enc.WriteU16(3334)
	enc.WriteB0_255("TestVendor")
	enc.WriteB0_255("1.0")
	enc.WriteB0_255("2.0")
	payload := enc.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DecodeSetupConnection(payload)
	}
}

func BenchmarkDecodeSubmitSharesStandard(b *testing.B) {
	enc := NewEncoder()
	enc.WriteU32(1)
	enc.WriteU32(1)
	enc.WriteU32(42)
	enc.WriteU32(0xDEADBEEF)
	enc.WriteU32(1609459200)
	enc.WriteU32(0x20000000)
	payload := enc.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DecodeSubmitSharesStandard(payload)
	}
}

func BenchmarkEncodeNewMiningJob(b *testing.B) {
	var merkleRoot [32]byte
	for i := range merkleRoot {
		merkleRoot[i] = byte(i * 2)
	}

	ntime := uint32(1609459200)
	msg := &NewMiningJob{
		ChannelID:  1,
		JobID:      42,
		MinNTime:   &ntime,
		Version:    0x20000000,
		MerkleRoot: merkleRoot,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncodeNewMiningJob(msg)
	}
}

// TestUserIdentityValidation tests the security validation of user identity strings.
func TestUserIdentityValidation(t *testing.T) {
	tests := []struct {
		name     string
		identity string
		valid    bool
	}{
		// Valid identities
		{"valid simple", "DGBaddress.worker1", true},
		{"valid with dash", "DGBaddress.worker-1", true},
		{"valid with underscore", "DGBaddress.worker_1", true},
		{"valid alphanumeric", "abc123XYZ", true},
		{"valid dots only", "a.b.c", true},

		// Invalid identities
		{"empty", "", false},
		{"with semicolon", "worker;rm -rf /", false},
		{"with backtick", "worker`id`", false},
		{"with dollar", "worker$(whoami)", false},
		{"with pipe", "worker|cat", false},
		{"with ampersand", "worker&&cat", false},
		{"with quotes", "worker'test", false},
		{"with double quotes", "worker\"test", false},
		{"with newline", "worker\ntest", false},
		{"with space", "worker test", false},
		{"with angle brackets", "worker<script>", false},
		{"with colon", "worker:test", true},
		{"with slash", "worker/test", false},
		{"with backslash", "worker\\test", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid := len(tt.identity) > 0 &&
				len(tt.identity) <= maxUserIdentityLen &&
				validUserIdentity.MatchString(tt.identity)
			if valid != tt.valid {
				t.Errorf("validUserIdentity(%q) = %v, want %v", tt.identity, valid, tt.valid)
			}
		})
	}
}
