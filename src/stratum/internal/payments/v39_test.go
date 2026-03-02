// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package payments

import (
	"context"
	"strings"
	"testing"
)

// TestV39_BlockMaturityTooLow verifies that Start() with a dangerously low
// blockMaturity logs a warning but does NOT return an error.
// This allows regtest configs with low maturity for fast testing.
func TestV39_BlockMaturityTooLow(t *testing.T) {
	t.Parallel()

	store := &mockBlockStore{}
	rpc := &mockDaemonRPC{
		bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		chainHeight:   1000,
		blockHashes:   make(map[uint64]string),
	}
	p := newTestProcessor(store, rpc, 5) // dangerously low — should warn, not error

	err := p.Start(context.Background())
	defer func() { _ = p.Stop() }()

	if err != nil {
		t.Fatalf("Start() should warn (not error) for blockMaturity=5, got: %v", err)
	}
}

// TestV39_BlockMaturityMinimum verifies that Start() does NOT return a V39
// error when blockMaturity is exactly at the minimum threshold (10).
// Start() may return other errors (nil daemon/db) but NOT the V39 error.
func TestV39_BlockMaturityMinimum(t *testing.T) {
	t.Parallel()

	// Use mocks so the goroutine launched by Start() does not panic on nil db/daemon.
	store := &mockBlockStore{}
	rpc := &mockDaemonRPC{
		bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		chainHeight:   1000,
		blockHashes:   make(map[uint64]string),
	}
	p := newTestProcessor(store, rpc, 10) // exact minimum

	err := p.Start(context.Background())
	defer func() { _ = p.Stop() }()

	if err != nil && strings.Contains(err.Error(), "V39") {
		t.Errorf("Start() should NOT return V39 error for blockMaturity=10, got: %v", err)
	}
}

// TestV39_BlockMaturityDefault verifies that Start() does NOT return a V39
// error when blockMaturity is 0 (which uses the default of 100).
func TestV39_BlockMaturityDefault(t *testing.T) {
	t.Parallel()

	store := &mockBlockStore{}
	rpc := &mockDaemonRPC{
		bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		chainHeight:   1000,
		blockHashes:   make(map[uint64]string),
	}
	p := newTestProcessor(store, rpc, 0) // 0 means use default (100)

	err := p.Start(context.Background())
	defer func() { _ = p.Stop() }()

	if err != nil && strings.Contains(err.Error(), "V39") {
		t.Errorf("Start() should NOT return V39 error for blockMaturity=0 (default 100), got: %v", err)
	}
}

// TestV39_BlockMaturityNegative verifies that Start() does NOT return a V39
// error when blockMaturity is negative. getBlockMaturity() treats values <= 0
// as "use default" (100), so V39 should not fire.
func TestV39_BlockMaturityNegative(t *testing.T) {
	t.Parallel()

	store := &mockBlockStore{}
	rpc := &mockDaemonRPC{
		bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		chainHeight:   1000,
		blockHashes:   make(map[uint64]string),
	}
	p := newTestProcessor(store, rpc, -1) // negative => default (100)

	err := p.Start(context.Background())
	defer func() { _ = p.Stop() }()

	if err != nil && strings.Contains(err.Error(), "V39") {
		t.Errorf("Start() should NOT return V39 error for blockMaturity=-1 (default 100), got: %v", err)
	}
}
