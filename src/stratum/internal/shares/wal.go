// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - Write-Ahead Log for share persistence.
//
// The WAL is designed to minimize share loss during crashes or restarts by
// persisting shares to disk before they enter the in-memory pipeline. On
// startup, any uncommitted shares are replayed from the WAL.
//
// WAL Protocol:
// 1. Share submitted -> Write to WAL (fsync) -> Enqueue to ring buffer
// 2. Batch written to DB successfully -> Remove batch entries from WAL
// 3. On startup -> Replay any remaining WAL entries to ring buffer
//
// Design goal: minimize share loss through durable logging.
package shares

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

const (
	// WALMagic is the file magic bytes for WAL files
	WALMagic = "SPWAL001"
	// WALEntryMagic marks the start of each entry
	WALEntryMagic uint32 = 0x53504C45 // "SPLE"
	// WALCommitMagic marks a batch as committed (written to DB)
	WALCommitMagic uint32 = 0x434D4954 // "CMIT"
	// MaxWALSize before rotation (100MB)
	MaxWALSize = 100 * 1024 * 1024
	// WALSyncInterval for periodic fsync
	WALSyncInterval = 100 * time.Millisecond
)

// WAL provides crash-safe persistence for shares before database write.
type WAL struct {
	mu       sync.Mutex
	dir      string
	file     *os.File
	writer   *bufio.Writer
	logger   *zap.SugaredLogger
	poolID   string

	// Current file state
	currentSize int64
	entryCount  uint64

	// Sync control
	lastSync    time.Time
	syncTicker  *time.Ticker
	closeChan   chan struct{}
	closeOnce   sync.Once
	wg          sync.WaitGroup

	// Metrics
	written     uint64
	committed   uint64
	replayed    uint64
}

// WALEntry represents a single share entry in the WAL
type WALEntry struct {
	Sequence  uint64          `json:"seq"`
	Timestamp int64           `json:"ts"`
	Share     *protocol.Share `json:"share"`
}

// NewWAL creates a new Write-Ahead Log for share persistence.
func NewWAL(dir, poolID string, logger *zap.Logger) (*WAL, error) {
	// Ensure WAL directory exists
	walDir := filepath.Join(dir, "wal", poolID)
	if err := os.MkdirAll(walDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create WAL directory %s: %w", walDir, err)
	}

	wal := &WAL{
		dir:       walDir,
		poolID:    poolID,
		logger:    logger.Sugar().With("component", "wal", "poolId", poolID),
		closeChan: make(chan struct{}),
	}

	// Open or create current WAL file
	if err := wal.openWALFile(); err != nil {
		return nil, err
	}

	// Start periodic sync goroutine
	wal.syncTicker = time.NewTicker(WALSyncInterval)
	wal.wg.Add(1)
	go wal.syncLoop()

	wal.logger.Infow("WAL initialized",
		"dir", walDir,
		"entryCount", wal.entryCount,
	)

	return wal, nil
}

// openWALFile opens the current WAL file or creates a new one.
func (w *WAL) openWALFile() error {
	walPath := filepath.Join(w.dir, "current.wal")

	var err error
	w.file, err = os.OpenFile(walPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0640)
	if err != nil {
		return fmt.Errorf("failed to open WAL file: %w", err)
	}

	// Get file size
	stat, err := w.file.Stat()
	if err != nil {
		w.file.Close()
		return fmt.Errorf("failed to stat WAL file: %w", err)
	}
	w.currentSize = stat.Size()

	// If new file, write magic header
	if w.currentSize == 0 {
		if _, err := w.file.WriteString(WALMagic); err != nil {
			w.file.Close()
			return fmt.Errorf("failed to write WAL magic: %w", err)
		}
		w.currentSize = int64(len(WALMagic))
	}

	w.writer = bufio.NewWriterSize(w.file, 64*1024) // 64KB buffer
	return nil
}

// Write persists a share to the WAL before it enters the pipeline.
// This is the critical path - share is only considered received after WAL write.
func (w *WAL) Write(share *protocol.Share) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return fmt.Errorf("WAL not open")
	}

	// Create entry
	w.entryCount++
	entry := WALEntry{
		Sequence:  w.entryCount,
		Timestamp: time.Now().UnixNano(),
		Share:     share,
	}

	// Serialize entry
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal WAL entry: %w", err)
	}

	// Write entry: [magic:4][length:4][data:N]
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[0:4], WALEntryMagic)
	binary.LittleEndian.PutUint32(header[4:8], uint32(len(data)))

	if _, err := w.writer.Write(header); err != nil {
		return fmt.Errorf("failed to write WAL header: %w", err)
	}
	if _, err := w.writer.Write(data); err != nil {
		return fmt.Errorf("failed to write WAL data: %w", err)
	}

	w.currentSize += int64(8 + len(data))
	w.written++

	// FIX SH-H1: Flush to OS page cache immediately after each write.
	// Without this, shares sit in bufio's userspace buffer and are lost on crash.
	// Full fsync still happens at batch commit via CommitBatchVerified().
	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL buffer: %w", err)
	}

	// Check if rotation needed
	if w.currentSize >= MaxWALSize {
		if err := w.rotate(); err != nil {
			w.logger.Warnw("WAL rotation failed", "error", err)
		}
	}

	return nil
}

// Sync flushes the WAL to disk.
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.writer == nil || w.file == nil {
		return nil
	}

	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL buffer: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync WAL file: %w", err)
	}

	w.lastSync = time.Now()
	return nil
}

// CommitBatch marks a batch of shares as successfully written to database.
// After commit, these shares can be removed from the WAL on next rotation.
func (w *WAL) CommitBatch(shares []*protocol.Share) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}

	// Write commit marker with share count
	// Format: [commit_magic:4][count:4][timestamp:8]
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint32(buf[0:4], WALCommitMagic)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(shares)))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(time.Now().UnixNano()))

	if _, err := w.writer.Write(buf); err != nil {
		return fmt.Errorf("failed to write commit marker: %w", err)
	}

	w.currentSize += 16
	w.committed += uint64(len(shares))

	return nil
}

// CommitBatchVerified marks a batch as committed AND verifies the write by syncing.
// C-2 fix: This ensures the commit marker is durably persisted before returning.
// Returns true if commit was verified (synced to disk), false if sync failed.
func (w *WAL) CommitBatchVerified(shares []*protocol.Share) (committed bool, err error) {
	if err := w.CommitBatch(shares); err != nil {
		return false, err
	}

	// Force sync to verify commit is durable
	if err := w.Sync(); err != nil {
		return false, fmt.Errorf("commit written but sync failed: %w", err)
	}

	return true, nil
}

// Replay reads uncommitted shares from the WAL for recovery.
// Returns shares that were written but never committed (database didn't confirm).
func (w *WAL) Replay() ([]*protocol.Share, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	walPath := filepath.Join(w.dir, "current.wal")

	// Open for reading
	file, err := os.Open(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No WAL to replay
		}
		return nil, fmt.Errorf("failed to open WAL for replay: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)

	// Verify magic
	magic := make([]byte, len(WALMagic))
	if _, err := io.ReadFull(reader, magic); err != nil {
		if err == io.EOF {
			return nil, nil // Empty file
		}
		return nil, fmt.Errorf("failed to read WAL magic: %w", err)
	}
	if string(magic) != WALMagic {
		return nil, fmt.Errorf("invalid WAL magic: %s", magic)
	}

	// Read all entries and commit markers
	var entries []*WALEntry
	var committed uint64

	// AUDIT FIX (W-1): Use labeled break to exit the for loop from inside switch cases.
	// In Go, `break` inside a switch only breaks the switch, not the enclosing for loop.
	// Without the label, truncated entries cause the reader to continue from a misaligned
	// position, reading garbage as headers — leading to corrupt recovery or OOM (W-2).
	header := make([]byte, 8)
readLoop:
	for {
		_, err := io.ReadFull(reader, header)
		if err == io.EOF {
			break
		}
		if err != nil {
			w.logger.Warnw("WAL replay: truncated header", "error", err)
			break
		}

		magic := binary.LittleEndian.Uint32(header[0:4])

		switch magic {
		case WALEntryMagic:
			length := binary.LittleEndian.Uint32(header[4:8])
			// AUDIT FIX (W-2): Bounds check on entry length to prevent OOM.
			// A corrupt/misaligned WAL could produce a garbage length up to 4GB.
			// No single share entry should ever exceed 1MB.
			const maxWALEntrySize = 1 * 1024 * 1024 // 1MB
			if length > maxWALEntrySize {
				w.logger.Warnw("WAL replay: entry length exceeds sanity limit, stopping",
					"length", length, "max", maxWALEntrySize)
				break readLoop
			}
			data := make([]byte, length)
			if _, err := io.ReadFull(reader, data); err != nil {
				w.logger.Warnw("WAL replay: truncated entry", "error", err)
				break readLoop
			}

			var entry WALEntry
			if err := json.Unmarshal(data, &entry); err != nil {
				w.logger.Warnw("WAL replay: corrupt entry", "error", err)
				continue
			}
			entries = append(entries, &entry)

		case WALCommitMagic:
			count := binary.LittleEndian.Uint32(header[4:8])
			// Read remaining 8 bytes of commit record
			extra := make([]byte, 8)
			if _, err := io.ReadFull(reader, extra); err != nil {
				w.logger.Warnw("WAL replay: truncated commit", "error", err)
				break readLoop
			}
			committed += uint64(count)

		default:
			w.logger.Warnw("WAL replay: unknown magic, stopping", "magic", magic)
			break readLoop
		}
	}

	// Calculate uncommitted shares
	// Simple strategy: entries after the committed count need replay
	uncommitted := len(entries) - int(committed)
	if uncommitted <= 0 {
		w.logger.Infow("WAL replay complete - no uncommitted shares",
			"totalEntries", len(entries),
			"committed", committed,
		)
		return nil, nil
	}

	// Return the uncommitted shares (last N entries)
	startIdx := len(entries) - uncommitted
	shares := make([]*protocol.Share, 0, uncommitted)
	for i := startIdx; i < len(entries); i++ {
		if entries[i].Share != nil {
			shares = append(shares, entries[i].Share)
		}
	}

	w.replayed = uint64(len(shares))
	w.logger.Infow("WAL replay complete - recovering uncommitted shares",
		"totalEntries", len(entries),
		"committed", committed,
		"uncommitted", len(shares),
	)

	return shares, nil
}

// rotate creates a new WAL file, archiving the old one.
func (w *WAL) rotate() error {
	// Flush and close current file
	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush before rotation: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync before rotation: %w", err)
	}
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("failed to close before rotation: %w", err)
	}

	// Archive old file with timestamp
	oldPath := filepath.Join(w.dir, "current.wal")
	archivePath := filepath.Join(w.dir, fmt.Sprintf("archive_%d.wal", time.Now().UnixNano()))
	if err := os.Rename(oldPath, archivePath); err != nil {
		// Rename failed but file is already closed. Reopen to restore working state.
		if reopenErr := w.openWALFile(); reopenErr != nil {
			return fmt.Errorf("WAL rotation failed and recovery failed: rename: %w, reopen: %v", err, reopenErr)
		}
		return fmt.Errorf("WAL rotation failed (recovered): %w", err)
	}

	// Open new file
	if err := w.openWALFile(); err != nil {
		return err
	}

	// Clean up old archives (keep last 3)
	w.cleanupArchives()

	w.logger.Infow("WAL rotated",
		"archived", archivePath,
		"entriesRotated", w.entryCount,
	)

	w.entryCount = 0
	return nil
}

// cleanupArchives removes old WAL archives, keeping the most recent ones.
func (w *WAL) cleanupArchives() {
	pattern := filepath.Join(w.dir, "archive_*.wal")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}

	// Keep at most 3 archives
	if len(matches) > 3 {
		// filepath.Glob does not guarantee sort order — sort lexicographically
		// so archive_<unix_nano>.wal timestamps are in ascending order
		sort.Strings(matches)
		for i := 0; i < len(matches)-3; i++ {
			if err := os.Remove(matches[i]); err != nil {
				w.logger.Warnw("Failed to remove old WAL archive", "path", matches[i], "error", err)
			}
		}
	}
}

// syncLoop periodically syncs the WAL to disk.
func (w *WAL) syncLoop() {
	defer w.wg.Done()

	for {
		select {
		case <-w.closeChan:
			return
		case <-w.syncTicker.C:
			if err := w.Sync(); err != nil {
				w.logger.Warnw("Periodic WAL sync failed", "error", err)
			}
		}
	}
}

// Close shuts down the WAL, ensuring all data is flushed.
func (w *WAL) Close() error {
	// Stop sync loop (once-guarded to prevent double-close panic on channel)
	w.closeOnce.Do(func() {
		close(w.closeChan)
	})
	w.syncTicker.Stop()
	w.wg.Wait()

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.writer != nil {
		if err := w.writer.Flush(); err != nil {
			w.logger.Warnw("WAL flush on close failed", "error", err)
		}
	}

	if w.file != nil {
		if err := w.file.Sync(); err != nil {
			w.logger.Warnw("WAL sync on close failed", "error", err)
		}
		if err := w.file.Close(); err != nil {
			return fmt.Errorf("failed to close WAL file: %w", err)
		}
	}

	w.logger.Infow("WAL closed",
		"written", w.written,
		"committed", w.committed,
		"replayed", w.replayed,
	)

	return nil
}

// Stats returns WAL statistics.
type WALStats struct {
	Written   uint64
	Committed uint64
	Replayed  uint64
	FileSize  int64
}

func (w *WAL) Stats() WALStats {
	w.mu.Lock()
	defer w.mu.Unlock()

	return WALStats{
		Written:   w.written,
		Committed: w.committed,
		Replayed:  w.replayed,
		FileSize:  w.currentSize,
	}
}
