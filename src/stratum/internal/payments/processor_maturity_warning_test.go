// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package payments

import (
	"context"
	"testing"
)

// TestProcessor_Start_LowMaturity_WarnsNotFatals verifies that Start() with
// a dangerously low blockMaturity (e.g. 3) returns nil (warning only, not error).
// This is the V39 FIX: regtest configs need low maturity for fast testing,
// so we warn but don't block startup.
func TestProcessor_Start_LowMaturity_WarnsNotFatals(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	proc := newTestProcessor(store, rpc, 3) // Well below MinSafeBlockMaturity (10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := proc.Start(ctx)
	if err != nil {
		t.Fatalf("Start() with low maturity should warn, not error — got: %v", err)
	}

	// Verify the processor is running
	proc.mu.Lock()
	running := proc.running
	proc.mu.Unlock()
	if !running {
		t.Error("Processor should be running after Start() with low maturity")
	}

	// Clean up
	_ = proc.Stop()
}

// TestProcessor_Start_SafeMaturity_NoError verifies that Start() with a
// safe blockMaturity (100) returns nil without any issue.
func TestProcessor_Start_SafeMaturity_NoError(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := proc.Start(ctx)
	if err != nil {
		t.Fatalf("Start() with safe maturity should succeed — got: %v", err)
	}

	proc.mu.Lock()
	running := proc.running
	proc.mu.Unlock()
	if !running {
		t.Error("Processor should be running after Start()")
	}

	_ = proc.Stop()
}

// TestProcessor_Start_BoundaryMaturity verifies that Start() with
// blockMaturity exactly at MinSafeBlockMaturity (10) returns nil with no
// warning. The boundary value is considered safe.
func TestProcessor_Start_BoundaryMaturity(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	// Exactly at the boundary — should NOT trigger the warning
	proc := newTestProcessor(store, rpc, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := proc.Start(ctx)
	if err != nil {
		t.Fatalf("Start() at boundary maturity should succeed — got: %v", err)
	}

	proc.mu.Lock()
	running := proc.running
	proc.mu.Unlock()
	if !running {
		t.Error("Processor should be running after Start()")
	}

	_ = proc.Stop()
}
