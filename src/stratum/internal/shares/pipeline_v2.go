// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - V2 pipeline with per-pool support.
//
// This provides a V2-compatible share pipeline that supports
// per-pool share tables for multi-coin operation.
package shares

import (
	"context"
	"time"

	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/pkg/protocol"
	"github.com/spiralpool/stratum/pkg/ringbuffer"
	"go.uber.org/zap"
)

// DefaultWALPath is the default data directory for WAL storage.
// Uses relative path so it works from any install directory (working dir set by systemd).
const DefaultWALPath = "./data"

// NewPipelineForPool creates a new share pipeline for a specific pool.
// It uses a pool-specific writer that directs shares to the correct table.
// CRITICAL: Enables WAL for crash recovery to minimize share loss.
func NewPipelineForPool(poolID string, db *database.PostgresDB, logger *zap.Logger) *Pipeline {
	// Create a pool-specific writer that writes to pool-specific tables
	writer := &PoolShareWriter{
		poolID: poolID,
		db:     db,
	}

	return &Pipeline{
		logger:         logger.Sugar().With("poolId", poolID),
		buffer:         ringbuffer.New[*protocol.Share](1 << 20), // 1M capacity
		batchSize:      1000,
		flushInterval:  5 * time.Second,
		batchChan:      make(chan []*protocol.Share, 100),
		writer:         writer,
		circuitBreaker: database.NewCircuitBreaker(database.DefaultCircuitBreakerConfig()),
		// WAL configuration for crash recovery
		walPath: DefaultWALPath,
		poolID:  poolID,
	}
}

// PoolShareWriter writes shares to pool-specific tables.
// It implements the ShareWriter interface.
type PoolShareWriter struct {
	poolID string
	db     *database.PostgresDB
}

// WriteBatch writes a batch of shares to the pool-specific table.
func (w *PoolShareWriter) WriteBatch(ctx context.Context, shares []*protocol.Share) error {
	// AUDIT FIX (SP-F2): Use WriteBatchForPool with explicit poolID.
	// The shared db instance has db.poolID set to the FIRST coin's pool ID
	// (coordinator.go creates one PostgresDB for all coins). Without this fix,
	// all V2 shares route to the first coin's table regardless of which coin
	// the PoolShareWriter was created for.
	return w.db.WriteBatchForPool(ctx, w.poolID, shares)
}

// Close closes the writer (no-op for this implementation).
func (w *PoolShareWriter) Close() error {
	return nil
}
