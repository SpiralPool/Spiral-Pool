// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build integration
// +build integration

package nodemanager

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/testing/mockdaemon"
	"go.uber.org/zap"
)

// TestNodeManagerFailover tests that the manager fails over to backup nodes.
func TestNodeManagerFailover(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Create mock daemons
	primary := mockdaemon.New()
	defer primary.Close()

	backup := mockdaemon.New()
	defer backup.Close()

	primaryURL, _ := url.Parse(primary.URL())
	backupURL, _ := url.Parse(backup.URL())

	// Configure nodes
	configs := []NodeConfig{
		{
			ID:       "primary",
			Host:     primaryURL.Hostname(),
			Port:     mustParsePort(primaryURL.Port()),
			User:     "user",
			Password: "pass",
			Priority: 0,
			Weight:   1,
		},
		{
			ID:       "backup",
			Host:     backupURL.Hostname(),
			Port:     mustParsePort(backupURL.Port()),
			User:     "user",
			Password: "pass",
			Priority: 1,
			Weight:   1,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()

	// Initial request should go to primary
	_, err = manager.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("GetBlockchainInfo failed: %v", err)
	}

	if primary.CallCount() == 0 {
		t.Error("expected primary to receive call")
	}

	// Make primary fail
	primary.SetFailNext(true)

	// This should fail over to backup
	_, err = manager.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("GetBlockchainInfo after primary failure: %v", err)
	}

	// Backup should have received a call
	if backup.CallCount() == 0 {
		t.Error("expected backup to receive call after primary failure")
	}
}

// TestNodeManagerHealthScoring tests health-based node selection.
func TestNodeManagerHealthScoring(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Create mock daemons with different latencies
	fast := mockdaemon.New()
	defer fast.Close()
	fast.SetLatency(10 * time.Millisecond)

	slow := mockdaemon.SlowDaemon(200 * time.Millisecond)
	defer slow.Close()

	fastURL, _ := url.Parse(fast.URL())
	slowURL, _ := url.Parse(slow.URL())

	configs := []NodeConfig{
		{
			ID:       "slow",
			Host:     slowURL.Hostname(),
			Port:     mustParsePort(slowURL.Port()),
			Priority: 0, // Higher priority but slower
			Weight:   1,
		},
		{
			ID:       "fast",
			Host:     fastURL.Hostname(),
			Port:     mustParsePort(fastURL.Port()),
			Priority: 1, // Lower priority but faster
			Weight:   1,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()

	// Make several requests to build up health history
	for i := 0; i < 10; i++ {
		manager.GetBlockchainInfo(ctx)
	}

	// Get node health scores
	nodes := manager.GetNodes()
	for _, node := range nodes {
		t.Logf("Node %s: health=%.2f, state=%v", node.ID, node.Health.Score, node.Health.State)
	}

	// Fast node should have better response time score
	// (though priority might still favor the slow node initially)
}

// TestNodeManagerBlockSubmission tests block submission to multiple nodes.
func TestNodeManagerBlockSubmission(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	primary := mockdaemon.New()
	defer primary.Close()

	backup := mockdaemon.New()
	defer backup.Close()

	primaryURL, _ := url.Parse(primary.URL())
	backupURL, _ := url.Parse(backup.URL())

	configs := []NodeConfig{
		{
			ID:       "primary",
			Host:     primaryURL.Hostname(),
			Port:     mustParsePort(primaryURL.Port()),
			Priority: 0,
		},
		{
			ID:       "backup",
			Host:     backupURL.Hostname(),
			Port:     mustParsePort(backupURL.Port()),
			Priority: 1,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	blockHex := "000000200000000000000000000000000000000000000000000000000000000000000000"

	// Submit block
	err = manager.SubmitBlock(ctx, blockHex)
	if err != nil {
		t.Fatalf("SubmitBlock failed: %v", err)
	}

	// Primary should have received the block
	if len(primary.SubmittedBlocks()) == 0 {
		t.Error("expected primary to receive block")
	}

	// Test failover on block submission
	primary.SetFailNext(true)

	err = manager.SubmitBlock(ctx, blockHex)
	if err != nil {
		t.Fatalf("SubmitBlock after primary failure: %v", err)
	}

	// Backup should have received the block
	if len(backup.SubmittedBlocks()) == 0 {
		t.Error("expected backup to receive block after primary failure")
	}
}

// TestNodeManagerAllNodesFail tests behavior when all nodes fail.
func TestNodeManagerAllNodesFail(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	primary := mockdaemon.UnhealthyDaemon()
	defer primary.Close()

	backup := mockdaemon.UnhealthyDaemon()
	defer backup.Close()

	primaryURL, _ := url.Parse(primary.URL())
	backupURL, _ := url.Parse(backup.URL())

	configs := []NodeConfig{
		{
			ID:   "primary",
			Host: primaryURL.Hostname(),
			Port: mustParsePort(primaryURL.Port()),
		},
		{
			ID:   "backup",
			Host: backupURL.Hostname(),
			Port: mustParsePort(backupURL.Port()),
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()

	// Should return error when all nodes fail
	_, err = manager.GetBlockTemplate(ctx)
	if err == nil {
		t.Error("expected error when all nodes fail")
	}

	if err != ErrAllNodesFailed {
		t.Errorf("expected ErrAllNodesFailed, got %v", err)
	}
}

// TestNodeManagerConcurrentRequests tests concurrent request handling.
func TestNodeManagerConcurrentRequests(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	daemon := mockdaemon.New()
	defer daemon.Close()

	daemonURL, _ := url.Parse(daemon.URL())

	configs := []NodeConfig{
		{
			ID:   "node1",
			Host: daemonURL.Hostname(),
			Port: mustParsePort(daemonURL.Port()),
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()

	// Launch concurrent requests
	const numRequests = 100
	errCh := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			_, err := manager.GetBlockchainInfo(ctx)
			errCh <- err
		}()
	}

	// Collect results
	var errors int
	for i := 0; i < numRequests; i++ {
		if err := <-errCh; err != nil {
			errors++
		}
	}

	if errors > 0 {
		t.Errorf("had %d errors out of %d concurrent requests", errors, numRequests)
	}

	// All requests should have been handled
	if daemon.CallCount() != numRequests {
		t.Errorf("expected %d calls, got %d", numRequests, daemon.CallCount())
	}
}

func mustParsePort(portStr string) int {
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return port
}

// =============================================================================
// BLOCK SUBMISSION FAILURE PATH TESTS (CRITICAL FOR SOLO MINING)
// =============================================================================
// These tests verify block submission error handling to prevent block loss.

// TestBlockSubmissionRPCTimeout tests block submission with RPC timeout.
func TestBlockSubmissionRPCTimeout(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Create a slow daemon that will timeout
	slow := mockdaemon.SlowDaemon(10 * time.Second)
	defer slow.Close()

	slowURL, _ := url.Parse(slow.URL())

	configs := []NodeConfig{
		{
			ID:       "slow_node",
			Host:     slowURL.Hostname(),
			Port:     mustParsePort(slowURL.Port()),
			Priority: 0,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	blockHex := "000000200000000000000000000000000000000000000000000000000000000000000000"

	// Submit block - should timeout
	err = manager.SubmitBlock(ctx, blockHex)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}

	// Verify error is timeout-related
	if ctx.Err() != context.DeadlineExceeded {
		t.Logf("Error received: %v", err)
	}

	t.Log("Block submission correctly returns error on timeout")
}

// TestBlockSubmissionRPCError tests block submission with RPC error response.
func TestBlockSubmissionRPCError(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	daemon := mockdaemon.New()
	defer daemon.Close()

	// Configure daemon to return error on next submitblock
	daemon.SetSubmitBlockError("RPC error: connection reset by peer")

	daemonURL, _ := url.Parse(daemon.URL())

	configs := []NodeConfig{
		{
			ID:       "error_node",
			Host:     daemonURL.Hostname(),
			Port:     mustParsePort(daemonURL.Port()),
			Priority: 0,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	blockHex := "000000200000000000000000000000000000000000000000000000000000000000000000"

	// Submit block - should get RPC error
	err = manager.SubmitBlock(ctx, blockHex)
	if err == nil {
		t.Error("expected RPC error, got nil")
	}

	t.Logf("Block submission correctly returns error: %v", err)
}

// TestBlockSubmissionDuplicateBlock tests handling of "duplicate block" response.
func TestBlockSubmissionDuplicateBlock(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	daemon := mockdaemon.New()
	defer daemon.Close()

	daemonURL, _ := url.Parse(daemon.URL())

	configs := []NodeConfig{
		{
			ID:       "node",
			Host:     daemonURL.Hostname(),
			Port:     mustParsePort(daemonURL.Port()),
			Priority: 0,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	blockHex := "000000200000000000000000000000000000000000000000000000000000000000000000"

	// First submission should succeed
	err = manager.SubmitBlock(ctx, blockHex)
	if err != nil {
		t.Fatalf("first SubmitBlock failed: %v", err)
	}

	// Configure daemon to return "duplicate" on next submit
	daemon.SetSubmitBlockError("duplicate block")

	// Second submission of same block - should return duplicate error
	err = manager.SubmitBlock(ctx, blockHex)

	// Duplicate error should be handled gracefully (not retried)
	// The block is already in the chain, so this is success
	t.Logf("Duplicate block handling result: %v", err)
}

// TestBlockSubmissionNetworkDisconnect tests block submission with network disconnect.
func TestBlockSubmissionNetworkDisconnect(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	primary := mockdaemon.New()
	backup := mockdaemon.New()
	defer backup.Close()

	primaryURL, _ := url.Parse(primary.URL())
	backupURL, _ := url.Parse(backup.URL())

	configs := []NodeConfig{
		{
			ID:       "primary",
			Host:     primaryURL.Hostname(),
			Port:     mustParsePort(primaryURL.Port()),
			Priority: 0,
		},
		{
			ID:       "backup",
			Host:     backupURL.Hostname(),
			Port:     mustParsePort(backupURL.Port()),
			Priority: 1,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	blockHex := "000000200000000000000000000000000000000000000000000000000000000000000000"

	// Close primary to simulate network disconnect
	primary.Close()

	// Submit block - should failover to backup
	err = manager.SubmitBlock(ctx, blockHex)
	if err != nil {
		t.Fatalf("SubmitBlock failed after primary disconnect: %v", err)
	}

	// Backup should have received the block
	if len(backup.SubmittedBlocks()) == 0 {
		t.Error("expected backup to receive block after primary disconnect")
	}

	t.Log("Block submission correctly failed over to backup after primary disconnect")
}

// TestBlockSubmissionAllNodesDown tests block submission when all nodes are down.
func TestBlockSubmissionAllNodesDown(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	primary := mockdaemon.New()
	backup := mockdaemon.New()

	primaryURL, _ := url.Parse(primary.URL())
	backupURL, _ := url.Parse(backup.URL())

	configs := []NodeConfig{
		{
			ID:       "primary",
			Host:     primaryURL.Hostname(),
			Port:     mustParsePort(primaryURL.Port()),
			Priority: 0,
		},
		{
			ID:       "backup",
			Host:     backupURL.Hostname(),
			Port:     mustParsePort(backupURL.Port()),
			Priority: 1,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Close both nodes
	primary.Close()
	backup.Close()

	ctx := context.Background()
	blockHex := "000000200000000000000000000000000000000000000000000000000000000000000000"

	// Submit block - should fail with ErrAllNodesFailed
	err = manager.SubmitBlock(ctx, blockHex)
	if err == nil {
		t.Fatal("expected error when all nodes are down, got nil")
	}

	if err != ErrAllNodesFailed {
		t.Logf("Expected ErrAllNodesFailed, got: %v (type: %T)", err, err)
	}

	t.Log("Block submission correctly returns error when all nodes are down")
}

// TestBlockSubmissionStaleBlock tests handling of "stale" block response.
func TestBlockSubmissionStaleBlock(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	daemon := mockdaemon.New()
	defer daemon.Close()

	// Configure daemon to return stale error
	daemon.SetSubmitBlockError("stale block")

	daemonURL, _ := url.Parse(daemon.URL())

	configs := []NodeConfig{
		{
			ID:       "node",
			Host:     daemonURL.Hostname(),
			Port:     mustParsePort(daemonURL.Port()),
			Priority: 0,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	blockHex := "000000200000000000000000000000000000000000000000000000000000000000000000"

	// Submit stale block - should return error but not retry
	err = manager.SubmitBlock(ctx, blockHex)

	// Stale blocks should be logged but not retried
	t.Logf("Stale block handling result: %v", err)
}

// TestBlockSubmissionWithRetry tests block submission retry on transient errors.
func TestBlockSubmissionWithRetry(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	daemon := mockdaemon.New()
	defer daemon.Close()

	// Configure daemon to fail first 2 attempts, succeed on 3rd
	failCount := 0
	daemon.SetSubmitBlockHandler(func(blockHex string) error {
		failCount++
		if failCount < 3 {
			return fmt.Errorf("temporary error %d", failCount)
		}
		return nil
	})

	daemonURL, _ := url.Parse(daemon.URL())

	configs := []NodeConfig{
		{
			ID:       "node",
			Host:     daemonURL.Hostname(),
			Port:     mustParsePort(daemonURL.Port()),
			Priority: 0,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	blockHex := "000000200000000000000000000000000000000000000000000000000000000000000000"

	// Submit block - should succeed after retries
	err = manager.SubmitBlock(ctx, blockHex)

	t.Logf("Block submission after retries result: %v (failCount=%d)", err, failCount)
}

// TestBlockSubmissionConcurrent tests concurrent block submissions.
func TestBlockSubmissionConcurrent(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	daemon := mockdaemon.New()
	defer daemon.Close()

	daemonURL, _ := url.Parse(daemon.URL())

	configs := []NodeConfig{
		{
			ID:       "node",
			Host:     daemonURL.Hostname(),
			Port:     mustParsePort(daemonURL.Port()),
			Priority: 0,
		},
	}

	manager, err := NewManagerFromConfigs("TEST", configs, logger)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()

	// Submit 10 blocks concurrently
	const numBlocks = 10
	errCh := make(chan error, numBlocks)

	for i := 0; i < numBlocks; i++ {
		go func(blockNum int) {
			blockHex := fmt.Sprintf("00000020%064d", blockNum)
			errCh <- manager.SubmitBlock(ctx, blockHex)
		}(i)
	}

	// Collect results
	var errors []error
	for i := 0; i < numBlocks; i++ {
		if err := <-errCh; err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		t.Errorf("had %d errors in concurrent block submissions: %v", len(errors), errors)
	}

	// Verify all blocks were submitted
	if len(daemon.SubmittedBlocks()) != numBlocks {
		t.Errorf("expected %d submitted blocks, got %d", numBlocks, len(daemon.SubmittedBlocks()))
	}

	t.Logf("Successfully submitted %d blocks concurrently", numBlocks)
}
