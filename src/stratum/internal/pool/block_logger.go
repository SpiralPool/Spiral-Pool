// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// BlockLogger provides a dedicated, non-sampled logger for block events.
// ORPHAN FIX #7: Standard zap.NewProduction() uses sampling which can drop
// logs under high load. Block events are too critical to ever drop.
//
// This logger:
// - Never samples (every block event is logged)
// - Writes to both stdout and a dedicated block log file
// - Uses JSON format for easy parsing and analysis
// - Syncs immediately after each write
type BlockLogger struct {
	logger   *zap.Logger
	logFile  *os.File
	filePath string
}

// NewBlockLogger creates a dedicated block logger with no sampling.
func NewBlockLogger(logDir string) (*BlockLogger, error) {
	// Ensure log directory exists
	if err := os.MkdirAll(logDir, 0750); err != nil {
		return nil, err
	}

	// Create log file
	filePath := filepath.Join(logDir, "blocks.log")
	logFile, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return nil, err
	}

	// Create encoder config optimized for block logging
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "", // No caller info needed for block logs
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "event",
		StacktraceKey:  "",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create cores - one for file, one for stdout
	fileCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(logFile),
		zap.InfoLevel,
	)

	stdoutCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(os.Stdout),
		zap.InfoLevel,
	)

	// Combine cores - NO SAMPLING
	core := zapcore.NewTee(fileCore, stdoutCore)

	// Create logger with no sampling
	logger := zap.New(core,
		zap.AddCaller(),
		// NO sampling options - every log is written
	)

	return &BlockLogger{
		logger:   logger,
		logFile:  logFile,
		filePath: filePath,
	}, nil
}

// LogBlockFound logs a block found event with full details.
// This log is NEVER sampled and ALWAYS written.
func (bl *BlockLogger) LogBlockFound(
	height uint64,
	hash string,
	miner string,
	worker string,
	jobID string,
	networkDiff float64,
	shareDiff float64,
	reward float64,
) {
	bl.logger.Info("BLOCK_FOUND",
		zap.Uint64("height", height),
		zap.String("hash", hash),
		zap.String("miner", miner),
		zap.String("worker", worker),
		zap.String("job_id", jobID),
		zap.Float64("network_diff", networkDiff),
		zap.Float64("share_diff", shareDiff),
		zap.Float64("reward", reward),
	)
	bl.logger.Sync() // Immediate sync
}

// LogBlockSubmitted logs a successful block submission.
func (bl *BlockLogger) LogBlockSubmitted(
	height uint64,
	hash string,
	miner string,
	attempts int,
	latencyMs int64,
) {
	bl.logger.Info("BLOCK_SUBMITTED",
		zap.Uint64("height", height),
		zap.String("hash", hash),
		zap.String("miner", miner),
		zap.Int("attempts", attempts),
		zap.Int64("latency_ms", latencyMs),
	)
	bl.logger.Sync()
}

// LogBlockRejected logs a block rejection with full diagnostics.
func (bl *BlockLogger) LogBlockRejected(
	height uint64,
	hash string,
	miner string,
	reason string,
	errorMsg string,
	prevHash string,
	jobAgeMs int64,
) {
	bl.logger.Error("BLOCK_REJECTED",
		zap.Uint64("height", height),
		zap.String("hash", hash),
		zap.String("miner", miner),
		zap.String("reason", reason),
		zap.String("error", errorMsg),
		zap.String("prev_hash", prevHash),
		zap.Int64("job_age_ms", jobAgeMs),
	)
	bl.logger.Sync()
}

// LogBlockOrphaned logs a block that was orphaned (submitted but not accepted).
func (bl *BlockLogger) LogBlockOrphaned(
	height uint64,
	hash string,
	miner string,
	reason string,
) {
	bl.logger.Warn("BLOCK_ORPHANED",
		zap.Uint64("height", height),
		zap.String("hash", hash),
		zap.String("miner", miner),
		zap.String("reason", reason),
	)
	bl.logger.Sync()
}

// LogAuxBlockFound logs an auxiliary chain block found event.
func (bl *BlockLogger) LogAuxBlockFound(
	chain string,
	height uint64,
	hash string,
	miner string,
	reward float64,
) {
	bl.logger.Info("AUX_BLOCK_FOUND",
		zap.String("chain", chain),
		zap.Uint64("height", height),
		zap.String("hash", hash),
		zap.String("miner", miner),
		zap.Float64("reward", reward),
	)
	bl.logger.Sync()
}

// LogAuxBlockSubmitted logs an auxiliary chain block submission.
func (bl *BlockLogger) LogAuxBlockSubmitted(
	chain string,
	height uint64,
	hash string,
	accepted bool,
	rejectReason string,
) {
	if accepted {
		bl.logger.Info("AUX_BLOCK_SUBMITTED",
			zap.String("chain", chain),
			zap.Uint64("height", height),
			zap.String("hash", hash),
			zap.Bool("accepted", true),
		)
	} else {
		bl.logger.Warn("AUX_BLOCK_REJECTED",
			zap.String("chain", chain),
			zap.Uint64("height", height),
			zap.String("hash", hash),
			zap.Bool("accepted", false),
			zap.String("reason", rejectReason),
		)
	}
	bl.logger.Sync()
}

// FilePath returns the path to the block log file.
func (bl *BlockLogger) FilePath() string {
	return bl.filePath
}

// Close syncs and closes the block logger.
func (bl *BlockLogger) Close() error {
	bl.logger.Sync()
	if bl.logFile != nil {
		return bl.logFile.Close()
	}
	return nil
}
