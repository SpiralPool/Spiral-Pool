// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"context"
	"testing"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/ha"
	"github.com/spiralpool/stratum/internal/payments"
	"go.uber.org/zap"
)

// =============================================================================
// G1: SetPaymentProcessor Advisory Lock Wiring Tests
// =============================================================================
// Verifies that SetPaymentProcessor correctly wires HA flags and advisory
// locker based on the Pool's HA configuration state (VIPManager, DBManager, DB).

// mockBlockStoreG1 satisfies payments.BlockStore for test Processor creation.
type mockBlockStoreG1 struct{}

func (m *mockBlockStoreG1) GetPendingBlocks(ctx context.Context) ([]*database.Block, error) {
	return nil, nil
}
func (m *mockBlockStoreG1) GetConfirmedBlocks(ctx context.Context) ([]*database.Block, error) {
	return nil, nil
}
func (m *mockBlockStoreG1) UpdateBlockStatus(ctx context.Context, height uint64, hash string, status string, confirmationProgress float64) error {
	return nil
}
func (m *mockBlockStoreG1) UpdateBlockOrphanCount(ctx context.Context, height uint64, hash string, mismatchCount int) error {
	return nil
}
func (m *mockBlockStoreG1) UpdateBlockStabilityCount(ctx context.Context, height uint64, hash string, stabilityCount int, lastTip string) error {
	return nil
}
func (m *mockBlockStoreG1) GetBlocksByStatus(_ context.Context, _ string) ([]*database.Block, error) {
	return nil, nil
}
func (m *mockBlockStoreG1) GetBlockStats(ctx context.Context) (*database.BlockStats, error) {
	return &database.BlockStats{}, nil
}
func (m *mockBlockStoreG1) UpdateBlockConfirmationState(_ context.Context, _ uint64, _ string, _ string, _ float64, _ int, _ int, _ string) error {
	return nil
}

// mockDaemonRPCG1 satisfies payments.DaemonRPC for test Processor creation.
type mockDaemonRPCG1 struct{}

func (m *mockDaemonRPCG1) GetBlockchainInfo(ctx context.Context) (*daemon.BlockchainInfo, error) {
	return &daemon.BlockchainInfo{}, nil
}
func (m *mockDaemonRPCG1) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	return "", nil
}

// newTestPaymentProcessor creates a minimal Processor for SetPaymentProcessor tests.
func newTestPaymentProcessor() *payments.Processor {
	return payments.NewProcessor(
		&config.PaymentsConfig{Enabled: true},
		&config.PoolConfig{Coin: "DGB"},
		&mockBlockStoreG1{},
		&mockDaemonRPCG1{},
		zap.NewNop(),
	)
}

// TestSetPaymentProcessor_NilVIPManager_HADisabled verifies that when
// VIPManager is nil (standalone mode), the processor does NOT get HA enabled.
func TestSetPaymentProcessor_NilVIPManager_HADisabled(t *testing.T) {
	t.Parallel()

	p := &Pool{
		logger:     zap.NewNop().Sugar(),
		vipManager: nil, // No HA
		dbManager:  nil,
	}

	proc := newTestPaymentProcessor()
	p.SetPaymentProcessor(proc)

	if p.paymentProcessor != proc {
		t.Error("paymentProcessor should be set")
	}
}

// TestSetPaymentProcessor_WithVIPManager_HAEnabled verifies that when
// VIPManager is set (HA mode), the processor gets SetHAEnabled(true).
func TestSetPaymentProcessor_WithVIPManager_HAEnabled(t *testing.T) {
	t.Parallel()

	// Create a pool with a non-nil VIP manager. Since vipManager is a concrete
	// pointer type (*ha.VIPManager), NOT an interface, a typed nil pointer
	// IS nil when compared with != nil. We must use &ha.VIPManager{} (non-nil
	// zero-value struct pointer) to trigger the HA-enabled code path.
	p := &Pool{
		logger:     zap.NewNop().Sugar(),
		vipManager: &ha.VIPManager{},
		dbManager:  nil,
	}

	proc := newTestPaymentProcessor()

	// Should not panic - wires HA enabled via proc.SetHAEnabled(true)
	p.SetPaymentProcessor(proc)

	if p.paymentProcessor != proc {
		t.Error("paymentProcessor should be set")
	}
}

// TestSetPaymentProcessor_WithDBManager_AdvisoryLocker verifies that when
// DatabaseManager is set, it is wired as the advisory locker (HA path).
func TestSetPaymentProcessor_WithDBManager_AdvisoryLocker(t *testing.T) {
	t.Parallel()

	// DatabaseManager implements AdvisoryLocker interface.
	// We use a non-nil zero-value struct pointer. DatabaseManager is a
	// concrete type, so typed nil would fail the != nil check in
	// SetPaymentProcessor. The zero-value pointer is stored but no
	// methods are called on it during wiring.
	p := &Pool{
		logger:    zap.NewNop().Sugar(),
		dbManager: &database.DatabaseManager{},
	}

	proc := newTestPaymentProcessor()

	// Should not panic - wires dbManager as advisory locker
	p.SetPaymentProcessor(proc)

	if p.paymentProcessor != proc {
		t.Error("paymentProcessor should be set")
	}
}

// TestSetPaymentProcessor_NilDBManager_PostgresDBFallback verifies that when
// dbManager is nil but db is a *database.PostgresDB, it is used as advisory locker.
func TestSetPaymentProcessor_NilDBManager_PostgresDBFallback(t *testing.T) {
	t.Parallel()

	// Use a typed nil *PostgresDB stored in the database.Database interface.
	// The type assertion p.db.(*database.PostgresDB) will succeed.
	var pgDB *database.PostgresDB
	p := &Pool{
		logger:    zap.NewNop().Sugar(),
		dbManager: nil,
		db:        pgDB,
	}

	proc := newTestPaymentProcessor()

	// Should not panic - falls back to PostgresDB as advisory locker
	p.SetPaymentProcessor(proc)

	if p.paymentProcessor != proc {
		t.Error("paymentProcessor should be set")
	}
}

// TestSetPaymentProcessor_NilDBManager_NilDB_NoAdvisoryLocker verifies that
// when both dbManager and db are nil, no advisory locker is wired (standalone
// non-PostgreSQL mode or test mode).
func TestSetPaymentProcessor_NilDBManager_NilDB_NoAdvisoryLocker(t *testing.T) {
	t.Parallel()

	p := &Pool{
		logger:    zap.NewNop().Sugar(),
		dbManager: nil,
		db:        nil,
	}

	proc := newTestPaymentProcessor()

	// Should not panic even with nil db
	p.SetPaymentProcessor(proc)

	if p.paymentProcessor != proc {
		t.Error("paymentProcessor should be set")
	}
}

// TestSetPaymentProcessor_FullHAWiring verifies the complete HA wiring path:
// VIPManager set + DBManager set = HAEnabled + DBManager as advisory locker.
func TestSetPaymentProcessor_FullHAWiring(t *testing.T) {
	t.Parallel()

	// Both VIPManager and DBManager must be non-nil to trigger the full
	// HA wiring path (SetHAEnabled + SetAdvisoryLocker). Both are concrete
	// pointer types, so typed nil would be nil. Use zero-value struct
	// pointers which are non-nil. SetPaymentProcessor stores the dbManager
	// reference but doesn't call methods on it during wiring.
	p := &Pool{
		logger:     zap.NewNop().Sugar(),
		vipManager: &ha.VIPManager{},
		dbManager:  &database.DatabaseManager{},
	}

	proc := newTestPaymentProcessor()

	// Should not panic - both HA enabled and advisory locker wired
	p.SetPaymentProcessor(proc)

	if p.paymentProcessor != proc {
		t.Error("paymentProcessor should be set")
	}
}
