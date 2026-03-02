// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool — Interface definitions for CoinPool's core dependencies.
//
// These unexported interfaces decouple CoinPool from concrete implementations
// of the job manager, node manager, and database. Go structural typing ensures
// the existing concrete types (*jobs.ManagerV2, *nodemanager.Manager,
// *database.PostgresDB) satisfy these interfaces without modification.
//
// Primary motivation: enable production-path testing of handleBlock() —
// the most money-critical code path — by allowing mock injection.
package pool

import (
	"context"

	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/nodemanager"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// coinPoolJobManager abstracts the job manager methods called by CoinPool.
// Satisfied by *jobs.ManagerV2 (which embeds *jobs.Manager).
type coinPoolJobManager interface {
	SetJobCallback(callback func(*protocol.Job))
	OnBlockNotificationWithHash(ctx context.Context, newTipHash string)
	GetJob(id string) (*protocol.Job, bool)
	GetLastBlockHash() string
	HeightContext(parent context.Context) (context.Context, context.CancelFunc)
	Start(ctx context.Context) error
	GetCurrentJob() *protocol.Job
	// SOLO mining: set miner's wallet for direct coinbase routing
	SetSoloMinerAddress(address string) error
	// RefreshJob forces a new job to be generated and broadcast
	RefreshJob(ctx context.Context, force bool) error
}

// coinPoolNodeManager abstracts the node manager methods called by CoinPool.
// Satisfied by *nodemanager.Manager.
type coinPoolNodeManager interface {
	SetBlockHandler(handler func(blockHash []byte))
	SetZMQStatusHandler(handler func(status daemon.ZMQStatus))
	SubmitBlockWithVerification(ctx context.Context, blockHex string, blockHash string, height uint64, timeouts *daemon.SubmitTimeouts) *daemon.BlockSubmitResult
	GetBlockHash(ctx context.Context, height uint64) (string, error)
	Start(ctx context.Context) error
	Stop() error
	HasZMQ() bool
	GetBlockchainInfo(ctx context.Context) (*daemon.BlockchainInfo, error)
	IsZMQFailed() bool
	IsZMQStable() bool
	GetDifficulty(ctx context.Context) (float64, error)
	Stats() nodemanager.ManagerStats
	SubmitBlock(ctx context.Context, blockHex string) error
	GetBlock(ctx context.Context, blockHash string) (map[string]interface{}, error)
	GetPrimary() *nodemanager.ManagedNode
}

// coinPoolDB abstracts the database methods called by CoinPool.
// Satisfied by *database.PostgresDB.
type coinPoolDB interface {
	InsertBlockForPool(ctx context.Context, poolID string, block *database.Block) error
	UpdateBlockStatusForPool(ctx context.Context, poolID string, height uint64, hash string, status string, confirmationProgress float64) error
	GetBlocksByStatus(ctx context.Context, status string) ([]*database.Block, error)
	GetPoolHashrateForPool(ctx context.Context, poolID string, windowMinutes int, algorithm string) (float64, error)
	UpdatePoolStatsForPool(ctx context.Context, poolID string, stats *database.PoolStats) error
	GetPoolHashrate(ctx context.Context, windowMinutes int) (float64, error)
	CleanupStaleShares(ctx context.Context, retentionMinutes int) (int64, error)
}
