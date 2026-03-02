//go:build nozmq

// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package daemon provides stub ZMQ functionality when libzmq is not available.
// This file is compiled when the 'nozmq' build tag is set (e.g., on Windows
// or systems without libzmq installed).
package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// ErrZMQDisabled is returned when ZMQ operations are attempted but ZMQ is disabled
var ErrZMQDisabled = errors.New("ZMQ is disabled (built with nozmq tag)")

// ZMQStatus represents the current state of ZMQ
type ZMQStatus int

const (
	ZMQStatusDisabled ZMQStatus = iota
	ZMQStatusConnecting
	ZMQStatusHealthy
	ZMQStatusDegraded
	ZMQStatusFailed
)

func (s ZMQStatus) String() string {
	return "disabled"
}

// ZMQListener is a stub implementation when ZMQ is not available
type ZMQListener struct {
	logger *zap.SugaredLogger
}

// NewZMQListener creates a stub ZMQ listener
func NewZMQListener(cfg *config.ZMQConfig, logger *zap.Logger) *ZMQListener {
	return &ZMQListener{
		logger: logger.Sugar(),
	}
}

// SetBlockHandler is a no-op stub
func (z *ZMQListener) SetBlockHandler(handler func(blockHash []byte)) {}

// SetFallbackHandler is a no-op stub
func (z *ZMQListener) SetFallbackHandler(handler func(usePoll bool)) {}

// SetStatusChangeHandler is a no-op stub
func (z *ZMQListener) SetStatusChangeHandler(handler func(status ZMQStatus)) {}

// Start returns an error indicating ZMQ is disabled
func (z *ZMQListener) Start(ctx context.Context) error {
	z.logger.Warn("ZMQ is disabled (built with nozmq tag), using polling only")
	return nil // Return nil to allow the pool to start with polling
}

// Stop is a no-op stub
func (z *ZMQListener) Stop() error {
	return nil
}

// Status always returns ZMQStatusDisabled
func (z *ZMQListener) Status() ZMQStatus {
	return ZMQStatusDisabled
}

// IsHealthy always returns false
func (z *ZMQListener) IsHealthy() bool {
	return false
}

// IsFailed always returns true to trigger polling fallback
func (z *ZMQListener) IsFailed() bool {
	return true
}

// IsRunning always returns false
func (z *ZMQListener) IsRunning() bool {
	return false
}

// ZMQStats holds ZMQ listener statistics
type ZMQStats struct {
	Status           string
	MessagesReceived uint64
	ErrorsCount      uint64
	LastMessageAge   time.Duration
	FailureDuration  time.Duration
	HealthyDuration  time.Duration
	StabilityReached bool
}

// Stats returns empty statistics
func (z *ZMQListener) Stats() ZMQStats {
	return ZMQStats{
		Status: "disabled",
	}
}

// TestConnection returns an error indicating ZMQ is disabled
func (z *ZMQListener) TestConnection(timeout time.Duration) error {
	return ErrZMQDisabled
}
