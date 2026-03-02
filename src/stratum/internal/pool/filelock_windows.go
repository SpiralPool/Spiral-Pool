// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build windows
// +build windows

package pool

import (
	"os"

	"golang.org/x/sys/windows"
)

// acquireFileLock acquires an exclusive, non-blocking file lock using LockFileEx.
// V11/V13 FIX: Prevents multiple pool processes from writing to the same WAL file.
// Returns an error if the lock is already held by another process.
// The lock is automatically released when the file is closed or the process exits.
//
// The lock is placed at a high byte offset (0x7FFFFFFF) rather than byte 0 so that
// the mandatory byte-range lock on Windows does not block normal reads/writes to the
// actual file data. This is purely an advisory-style exclusion mechanism.
func acquireFileLock(f *os.File) error {
	ol := new(windows.Overlapped)
	ol.Offset = 0x7FFFFFFF // lock a byte far beyond any real WAL data
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, // reserved
		1, // lock 1 byte
		0, // high-order bytes of length
		ol,
	)
}
