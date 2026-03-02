// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// MinDiskSpaceBytes is the minimum free disk space required before WAL write.
// If disk space falls below this threshold, WAL writes will fail-fast to prevent
// silent failures during fsync. Default: 100MB
const MinDiskSpaceBytes uint64 = 100 * 1024 * 1024 // 100MB

// BlockWAL provides write-ahead logging for block submissions.
// ORPHAN FIX #6: Designed to prevent block loss by maintaining a durable record.
//
// The WAL is designed so that before any block is submitted to the daemon,
// its complete data is persisted to disk. This allows recovery of blocks
// that were found but lost due to crashes, OOM kills, or other failures.
//
// WAL entries are append-only and never deleted automatically.
// Operators should archive old WAL files periodically.
type BlockWAL struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	logger   *zap.SugaredLogger
}

// BlockWALEntry represents a single block submission record.
type BlockWALEntry struct {
	// Timestamp when the block was found
	Timestamp time.Time `json:"timestamp"`

	// Block identification
	Height    uint64 `json:"height"`
	BlockHash string `json:"block_hash"`
	PrevHash  string `json:"prev_hash"`

	// Full block data for potential manual resubmission
	BlockHex string `json:"block_hex"`

	// Miner attribution
	MinerAddress string `json:"miner_address"`
	WorkerName   string `json:"worker_name"`

	// Job context
	JobID  string        `json:"job_id"`
	JobAge time.Duration `json:"job_age_ns"` // Stored as nanoseconds

	// Reward info
	CoinbaseValue int64 `json:"coinbase_value"` // Satoshis

	// Aux chain identification (only set for merge-mined aux blocks)
	AuxSymbol string `json:"aux_symbol,omitempty"`

	// Submission status (updated after submission attempt)
	// Possible values: "submitting" (pre-submission), "pending", "submitted", "accepted",
	// "rejected", "orphaned", "build_failed", "failed", "aux_submitting" (aux pre-submission)
	Status       string `json:"status"`
	SubmitError  string `json:"submit_error,omitempty"`
	SubmittedAt  string `json:"submitted_at,omitempty"`
	RejectReason string `json:"reject_reason,omitempty"`

	// Raw block components for manual reconstruction when BlockHex assembly fails.
	// If these are populated, an operator can rebuild the block using:
	//   header + varint(1+len(TxData)) + coinbase1+en1+en2+coinbase2 + txdata...
	// then submit via: bitcoin-cli submitblock <hex>
	CoinBase1      string   `json:"coinbase1,omitempty"`
	CoinBase2      string   `json:"coinbase2,omitempty"`
	ExtraNonce1    string   `json:"extranonce1,omitempty"`
	ExtraNonce2    string   `json:"extranonce2,omitempty"`
	Version        string   `json:"version,omitempty"`
	NBits          string   `json:"nbits,omitempty"`
	NTime          string   `json:"ntime,omitempty"`
	Nonce          string   `json:"nonce,omitempty"`
	TransactionData []string `json:"transaction_data,omitempty"`
}

// NewBlockWAL creates a new block write-ahead log.
// The WAL file is created in the specified directory with a timestamped name.
func NewBlockWAL(dataDir string, logger *zap.Logger) (*BlockWAL, error) {
	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create WAL directory: %w", err)
	}

	// Create WAL file with timestamp to allow rotation
	filename := fmt.Sprintf("block_wal_%s.jsonl", time.Now().Format("2006-01-02"))
	filePath := filepath.Join(dataDir, filename)

	// Open file in append mode, create if not exists.
	// O_RDWR is required instead of O_WRONLY because Windows LockFileEx needs
	// GENERIC_READ access on the file handle for exclusive locking to succeed.
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0640)
	if err != nil {
		return nil, fmt.Errorf("failed to open WAL file: %w", err)
	}

	// V11/V13 FIX: Acquire exclusive file lock to prevent multiple processes
	// from writing to the same WAL file. If another process holds the lock,
	// this instance must refuse to start to prevent WAL corruption and
	// duplicate block submissions. Lock is automatically released on process exit.
	if err := acquireFileLock(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to acquire WAL file lock (is another instance running?): %w", err)
	}

	// V23 FIX: Sync directory metadata to ensure WAL file entry is durable.
	// Without this, a power failure immediately after file creation could lose
	// the directory entry even though file.Sync() succeeded on the data.
	if dir, dirErr := os.Open(dataDir); dirErr == nil {
		dir.Sync()
		dir.Close()
	}

	return &BlockWAL{
		file:     file,
		filePath: filePath,
		logger:   logger.Sugar(),
	}, nil
}

// checkDiskSpace verifies there's sufficient disk space for WAL writes.
// Returns an error if free space is below MinDiskSpaceBytes.
// This prevents silent failures during fsync on a full disk.
// Note: Uses platform-specific implementation via checkDiskSpaceAvailable().
func (w *BlockWAL) checkDiskSpace() error {
	dir := filepath.Dir(w.filePath)

	freeBytes, err := checkDiskSpaceAvailable(dir)
	if err != nil {
		// If we can't check disk space, log warning but allow write attempt
		w.logger.Warnw("Failed to check disk space (continuing anyway)",
			"path", dir,
			"error", err,
		)
		return nil
	}

	if freeBytes < MinDiskSpaceBytes {
		return fmt.Errorf("insufficient disk space for WAL write: %d bytes free, need %d bytes minimum",
			freeBytes, MinDiskSpaceBytes)
	}

	return nil
}

// LogBlockFound records a found block BEFORE submission.
// This MUST be called before SubmitBlock to ensure crash recovery is possible.
// Returns the entry for later status updates.
func (w *BlockWAL) LogBlockFound(entry *BlockWALEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// CRITICAL: Check disk space BEFORE attempting write
	// This prevents silent failures during fsync on full disk
	if err := w.checkDiskSpace(); err != nil {
		return fmt.Errorf("disk space check failed: %w", err)
	}

	entry.Timestamp = time.Now()
	if entry.Status == "" {
		entry.Status = "pending"
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal WAL entry: %w", err)
	}

	// Write entry with newline
	if _, err := w.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write WAL entry: %w", err)
	}

	// CRITICAL: Sync to disk immediately - this is the durability guarantee
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync WAL: %w", err)
	}

	// Safe hash preview - avoid panic on short/empty hash
	hashPreview := entry.BlockHash
	if len(hashPreview) > 16 {
		hashPreview = hashPreview[:16] + "..."
	}
	w.logger.Infow("📝 Block recorded in WAL (pre-submission)",
		"height", entry.Height,
		"hash", hashPreview,
		"walFile", filepath.Base(w.filePath),
	)

	return nil
}

// LogSubmissionResult appends the submission result to the WAL.
// This creates a new entry with the updated status for audit trail.
func (w *BlockWAL) LogSubmissionResult(entry *BlockWALEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// AUDIT FIX: Check disk space before post-submission write (parity with LogBlockFound).
	// A full disk during result write leaves the entry stuck in "submitting" state,
	// which triggers unnecessary recovery on next startup.
	if err := w.checkDiskSpace(); err != nil {
		w.logger.Warnw("Disk space check failed for WAL result write (continuing anyway)",
			"error", err,
			"blockHash", entry.BlockHash,
			"status", entry.Status,
		)
		// Continue anyway — the result entry is less critical than the pre-submission entry.
		// Missing a result entry causes a benign re-verification on recovery, not block loss.
	}

	entry.SubmittedAt = time.Now().Format(time.RFC3339Nano)

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal WAL result entry: %w", err)
	}

	if _, err := w.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write WAL result entry: %w", err)
	}

	// Sync result to disk
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync WAL result: %w", err)
	}

	return nil
}

// FlushToDisk forces an fsync on the WAL file to ensure all buffered writes
// are persisted. Called during HA role transitions to prevent data loss.
func (w *BlockWAL) FlushToDisk() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		return w.file.Sync()
	}
	return nil
}

// Close closes the WAL file.
func (w *BlockWAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file != nil {
		if err := w.file.Sync(); err != nil {
			w.logger.Warnw("Failed to sync WAL on close", "error", err)
		}
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

// FilePath returns the current WAL file path.
func (w *BlockWAL) FilePath() string {
	return w.filePath
}

// RecoverUnsubmittedBlocks reads the WAL and returns entries that were
// recorded as "pending" but never got a submission result.
// This should be called at startup for crash recovery.
func RecoverUnsubmittedBlocks(dataDir string) ([]BlockWALEntry, error) {
	// Find all WAL files
	pattern := filepath.Join(dataDir, "block_wal_*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to glob WAL files: %w", err)
	}

	// Track blocks by hash to find unsubmitted ones
	// Map: blockHash -> latest entry
	blockStates := make(map[string]*BlockWALEntry)

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			// V8 FIX: Log skipped files instead of silently continuing.
			zap.S().Warnw("WAL recovery: skipping unreadable file",
				"file", file,
				"error", err,
			)
			continue
		}

		// Parse JSONL (one JSON object per line)
		lines := splitLines(data)
		for lineIdx, line := range lines {
			if len(line) == 0 {
				continue
			}
			var entry BlockWALEntry
			if err := json.Unmarshal(line, &entry); err != nil {
				// R-9 FIX: Distinguish truncated trailing lines (power loss mid-write)
				// from genuinely corrupt entries in the middle of the file.
				if lineIdx == len(lines)-1 && !endsWithNewline(data) {
					zap.S().Warnw("WAL recovery: truncated trailing entry (likely power loss mid-write)",
						"file", file,
						"lineBytes", len(line),
						"error", err,
					)
				} else {
					// V8 FIX: Log malformed entries instead of silently continuing.
					zap.S().Warnw("WAL recovery: skipping malformed entry",
						"file", file,
						"lineIndex", lineIdx,
						"error", err,
					)
				}
				continue
			}
			blockStates[entry.BlockHash] = &entry
		}
	}

	// Find blocks still in "pending", "submitting", or "aux_submitting" status.
	// "submitting" entries are written by the P0 pre-submission WAL write:
	// if the process crashes during SubmitBlockWithVerification, the block
	// will have status "submitting" with no subsequent status update.
	// "aux_submitting" entries are the V1 merge mining equivalent: written
	// before SubmitAuxBlock, indicating an aux block submission was in-flight.
	var unsubmitted []BlockWALEntry
	for _, entry := range blockStates {
		if entry.Status == "pending" || entry.Status == "submitting" || entry.Status == "aux_submitting" {
			unsubmitted = append(unsubmitted, *entry)
		}
	}

	// AUDIT FIX: Sort by timestamp ascending so oldest blocks are replayed first.
	// Map iteration order is random in Go; without sorting, a newer block could be
	// resubmitted before an older one, causing the older block to be stale by the
	// time it's attempted.
	sort.Slice(unsubmitted, func(i, j int) bool {
		return unsubmitted[i].Timestamp.Before(unsubmitted[j].Timestamp)
	})

	return unsubmitted, nil
}

// RecoverSubmittedBlocks reads the WAL and returns entries with terminal
// success status ("submitted" or "accepted"). These are blocks that were
// successfully sent to the daemon but may not have corresponding DB records.
// V25 FIX: Used at startup for WAL-DB reconciliation to ensure every
// successfully submitted block has a database record for payment processing.
func RecoverSubmittedBlocks(dataDir string) ([]BlockWALEntry, error) {
	pattern := filepath.Join(dataDir, "block_wal_*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to glob WAL files: %w", err)
	}

	// Track blocks by hash — latest entry wins
	blockStates := make(map[string]*BlockWALEntry)

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			zap.S().Warnw("WAL reconciliation: skipping unreadable file",
				"file", file,
				"error", err,
			)
			continue
		}

		lines := splitLines(data)
		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			var entry BlockWALEntry
			if err := json.Unmarshal(line, &entry); err != nil {
				continue // Malformed entries already logged by RecoverUnsubmittedBlocks
			}
			blockStates[entry.BlockHash] = &entry
		}
	}

	// Return blocks with terminal success status.
	// "aux_pending" is the merge mining equivalent of "submitted": the aux block
	// was successfully sent to the aux daemon but may not have a DB record yet.
	var submitted []BlockWALEntry
	for _, entry := range blockStates {
		if entry.Status == "submitted" || entry.Status == "accepted" || entry.Status == "aux_pending" {
			submitted = append(submitted, *entry)
		}
	}

	// AUDIT FIX: Sort by timestamp ascending for deterministic replay order.
	sort.Slice(submitted, func(i, j int) bool {
		return submitted[i].Timestamp.Before(submitted[j].Timestamp)
	})

	return submitted, nil
}

// endsWithNewline returns true if the data ends with a newline character.
// Used to detect truncated WAL files where the last line was mid-write.
func endsWithNewline(data []byte) bool {
	n := len(data)
	if n == 0 {
		return false
	}
	return data[n-1] == '\n'
}

// splitLines splits data by newlines, handling both \n and \r\n
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			end := i
			if end > start && data[end-1] == '\r' {
				end--
			}
			lines = append(lines, data[start:end])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// ScanPendingEntries returns WAL entries still in pending/submitting state
// along with their age. Used by Sentinel for stuck-entry alerting.
// This is a thin wrapper around RecoverUnsubmittedBlocks to keep Sentinel's
// contract clean and allow future optimization (e.g., caching).
func ScanPendingEntries(dataDir string) ([]BlockWALEntry, error) {
	return RecoverUnsubmittedBlocks(dataDir)
}

// CountWALFiles returns the number of WAL files in the given directory.
// Used by Sentinel for file count monitoring.
func CountWALFiles(dataDir string) (int, error) {
	pattern := filepath.Join(dataDir, "block_wal_*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("failed to glob WAL files: %w", err)
	}
	return len(files), nil
}

// DefaultWALRetentionDays is the default number of days to retain WAL files.
// WAL files older than this are deleted during cleanup. 30 days is conservative:
// any unsubmitted block older than 30 days is guaranteed stale on all supported chains.
const DefaultWALRetentionDays = 30

// CleanupOldWALFiles removes WAL files older than the specified retention period.
// This should be called AFTER RecoverUnsubmittedBlocks and RecoverSubmittedBlocks
// have completed, so that all recoverable entries have already been processed.
//
// Files are identified by the date in their filename (block_wal_YYYY-MM-DD.jsonl).
// Returns the number of files cleaned up and any errors encountered.
func CleanupOldWALFiles(dataDir string, retentionDays int) (int, error) {
	if retentionDays <= 0 {
		retentionDays = DefaultWALRetentionDays
	}

	pattern := filepath.Join(dataDir, "block_wal_*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("failed to glob WAL files for cleanup: %w", err)
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	cleaned := 0

	for _, file := range files {
		// Extract date from filename: block_wal_2006-01-02.jsonl
		base := filepath.Base(file)
		// Strip prefix "block_wal_" and suffix ".jsonl"
		if len(base) < len("block_wal_2006-01-02.jsonl") {
			continue // Malformed filename, skip
		}
		dateStr := base[len("block_wal_") : len(base)-len(".jsonl")]
		fileDate, parseErr := time.Parse("2006-01-02", dateStr)
		if parseErr != nil {
			continue // Can't parse date, skip (don't delete what we don't understand)
		}

		if fileDate.Before(cutoff) {
			if removeErr := os.Remove(file); removeErr != nil {
				zap.S().Warnw("WAL cleanup: failed to remove old file",
					"file", file,
					"age", time.Since(fileDate).Hours()/24,
					"error", removeErr,
				)
			} else {
				zap.S().Infow("WAL cleanup: removed old file",
					"file", filepath.Base(file),
					"date", dateStr,
					"retentionDays", retentionDays,
				)
				cleaned++
			}
		}
	}

	return cleaned, nil
}
