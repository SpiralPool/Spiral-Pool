// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readLogLines reads the block log file, splits it into non-empty lines, and
// unmarshals each line into a map. The caller must have closed (or synced) the
// BlockLogger before calling this so that all data is flushed.
func readLogLines(t *testing.T, path string) []map[string]interface{} {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read log file %s: %v", path, err)
	}

	raw := strings.Split(strings.TrimSpace(string(data)), "\n")
	var lines []map[string]interface{}
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("failed to unmarshal log line: %v\nline: %s", err, line)
		}
		lines = append(lines, m)
	}
	return lines
}

// assertField checks that a JSON map contains the expected key with the
// expected value (compared as the JSON-decoded type: string, float64, bool).
func assertField(t *testing.T, m map[string]interface{}, key string, expected interface{}) {
	t.Helper()
	val, ok := m[key]
	if !ok {
		t.Errorf("expected key %q in JSON line, but it was missing. Line: %v", key, m)
		return
	}

	// json.Unmarshal stores numbers as float64, bools as bool, strings as string.
	switch exp := expected.(type) {
	case string:
		if s, ok := val.(string); !ok || s != exp {
			t.Errorf("key %q: expected %q, got %v", key, exp, val)
		}
	case float64:
		if f, ok := val.(float64); !ok || f != exp {
			t.Errorf("key %q: expected %v, got %v", key, exp, val)
		}
	case bool:
		if b, ok := val.(bool); !ok || b != exp {
			t.Errorf("key %q: expected %v, got %v", key, exp, val)
		}
	default:
		t.Errorf("assertField: unsupported expected type %T for key %q", expected, key)
	}
}

func TestBlockLogger_NewBlockLogger_CreatesDirectory(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")

	bl, err := NewBlockLogger(nested)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}
	defer bl.Close()

	// Verify the nested directory was created.
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("expected directory %s to exist: %v", nested, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", nested)
	}

	// Verify blocks.log exists.
	logPath := filepath.Join(nested, "blocks.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected blocks.log at %s: %v", logPath, err)
	}
}

func TestBlockLogger_NewBlockLogger_FilePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}
	defer bl.Close()

	expected := filepath.Join(dir, "blocks.log")
	if bl.FilePath() != expected {
		t.Errorf("FilePath() = %q, want %q", bl.FilePath(), expected)
	}
}

func TestBlockLogger_LogBlockFound_WritesJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	bl.LogBlockFound(100000, "00000000abc", "miner1.worker1", "rig01", "job42",
		1234567890.5, 9999999.1, 3.125)
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	m := lines[0]
	assertField(t, m, "event", "BLOCK_FOUND")
	assertField(t, m, "height", float64(100000))
	assertField(t, m, "hash", "00000000abc")
	assertField(t, m, "miner", "miner1.worker1")
	assertField(t, m, "worker", "rig01")
	assertField(t, m, "job_id", "job42")
	assertField(t, m, "network_diff", 1234567890.5)
	assertField(t, m, "share_diff", 9999999.1)
	assertField(t, m, "reward", 3.125)
	assertField(t, m, "level", "info")
}

func TestBlockLogger_LogBlockSubmitted_WritesJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	bl.LogBlockSubmitted(200000, "00000000def", "miner2", 3, 450)
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	m := lines[0]
	assertField(t, m, "event", "BLOCK_SUBMITTED")
	assertField(t, m, "height", float64(200000))
	assertField(t, m, "hash", "00000000def")
	assertField(t, m, "miner", "miner2")
	assertField(t, m, "attempts", float64(3))
	assertField(t, m, "latency_ms", float64(450))
	assertField(t, m, "level", "info")
}

func TestBlockLogger_LogBlockRejected_WritesJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	bl.LogBlockRejected(300000, "00000000ghi", "miner3", "stale-share",
		"job too old", "00000000prev", 5200)
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	m := lines[0]
	assertField(t, m, "event", "BLOCK_REJECTED")
	assertField(t, m, "height", float64(300000))
	assertField(t, m, "hash", "00000000ghi")
	assertField(t, m, "miner", "miner3")
	assertField(t, m, "reason", "stale-share")
	assertField(t, m, "error", "job too old")
	assertField(t, m, "prev_hash", "00000000prev")
	assertField(t, m, "job_age_ms", float64(5200))
	assertField(t, m, "level", "error")
}

func TestBlockLogger_LogBlockOrphaned_WritesJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	bl.LogBlockOrphaned(400000, "00000000jkl", "miner4", "chain-reorg")
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	m := lines[0]
	assertField(t, m, "event", "BLOCK_ORPHANED")
	assertField(t, m, "level", "warn")
}

func TestBlockLogger_LogAuxBlockFound_WritesJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	bl.LogAuxBlockFound("namecoin", 550000, "00000000mno", "miner5", 6.25)
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	m := lines[0]
	assertField(t, m, "event", "AUX_BLOCK_FOUND")
	assertField(t, m, "chain", "namecoin")
	assertField(t, m, "height", float64(550000))
	assertField(t, m, "hash", "00000000mno")
	assertField(t, m, "miner", "miner5")
	assertField(t, m, "reward", 6.25)
	assertField(t, m, "level", "info")
}

func TestBlockLogger_LogAuxBlockSubmitted_Accepted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	bl.LogAuxBlockSubmitted("namecoin", 550001, "00000000pqr", true, "")
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	m := lines[0]
	assertField(t, m, "event", "AUX_BLOCK_SUBMITTED")
	assertField(t, m, "accepted", true)
	assertField(t, m, "level", "info")
}

func TestBlockLogger_LogAuxBlockSubmitted_Rejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	bl.LogAuxBlockSubmitted("namecoin", 550002, "00000000stu", false, "duplicate-block")
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	m := lines[0]
	assertField(t, m, "event", "AUX_BLOCK_REJECTED")
	assertField(t, m, "accepted", false)
	assertField(t, m, "reason", "duplicate-block")
	assertField(t, m, "level", "warn")
}

func TestBlockLogger_Close_IdempotentFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	// First close should succeed.
	if err := bl.Close(); err != nil {
		t.Fatalf("first Close() returned error: %v", err)
	}

	// Second close may return an error (file already closed) but must not panic.
	// We just verify it doesn't panic; the error value is acceptable.
	_ = bl.Close()
}

func TestBlockLogger_MultipleEvents_AllWritten(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	bl.LogBlockFound(1, "hash1", "m1", "w1", "j1", 1.0, 1.0, 1.0)
	bl.LogBlockSubmitted(2, "hash2", "m2", 1, 100)
	bl.LogBlockOrphaned(3, "hash3", "m3", "reorg")
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) != 3 {
		t.Fatalf("expected 3 log lines, got %d", len(lines))
	}

	// Verify ordering matches the write order.
	assertField(t, lines[0], "event", "BLOCK_FOUND")
	assertField(t, lines[1], "event", "BLOCK_SUBMITTED")
	assertField(t, lines[2], "event", "BLOCK_ORPHANED")
}

func TestBlockLogger_NoSampling_ManyEvents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	const count = 200
	for i := 0; i < count; i++ {
		bl.LogBlockFound(uint64(i), "samehash", "sameminer", "sameworker",
			"samejob", 1.0, 1.0, 1.0)
	}
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) != count {
		t.Fatalf("expected %d log lines (no sampling), got %d", count, len(lines))
	}
}

func TestBlockLogger_JSONStructure_HasTimestamp(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bl, err := NewBlockLogger(dir)
	if err != nil {
		t.Fatalf("NewBlockLogger returned error: %v", err)
	}

	bl.LogBlockFound(1, "h", "m", "w", "j", 1.0, 1.0, 1.0)
	bl.LogBlockSubmitted(2, "h", "m", 1, 10)
	bl.LogBlockRejected(3, "h", "m", "r", "e", "ph", 100)
	bl.LogBlockOrphaned(4, "h", "m", "r")
	bl.Close()

	lines := readLogLines(t, bl.FilePath())
	if len(lines) == 0 {
		t.Fatal("expected at least one log line")
	}

	for i, m := range lines {
		ts, ok := m["ts"]
		if !ok {
			t.Errorf("line %d: missing 'ts' field", i)
			continue
		}
		tsStr, ok := ts.(string)
		if !ok {
			t.Errorf("line %d: 'ts' is not a string: %v", i, ts)
			continue
		}
		// ISO8601 timestamps produced by zap start with the year, e.g. "2026-..."
		if !strings.HasPrefix(tsStr, "20") {
			t.Errorf("line %d: 'ts' does not look like ISO8601 (expected prefix '20'): %q", i, tsStr)
		}
	}
}

func TestBlockLogger_NewBlockLogger_InvalidPath(t *testing.T) {
	t.Parallel()

	// Use a path that cannot be created on any OS.
	// On Windows the NUL device cannot be a directory parent; on Unix
	// /dev/null is a regular file so MkdirAll inside it will fail.
	invalidDir := filepath.Join(string(os.DevNull), "impossible", "path")

	bl, err := NewBlockLogger(invalidDir)
	if err == nil {
		// If somehow it did not fail, close and clean up.
		bl.Close()
		t.Fatal("expected error for invalid path, got nil")
	}
}
