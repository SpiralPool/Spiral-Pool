// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build linux || darwin || freebsd || openbsd || netbsd
// +build linux darwin freebsd openbsd netbsd

package pool

import (
	"os"

	"golang.org/x/sys/unix"
)

// acquireFileLock acquires an exclusive, non-blocking file lock (flock).
// V11/V13 FIX: Prevents multiple pool processes from writing to the same WAL file.
// Returns an error if the lock is already held by another process.
// The lock is automatically released when the file is closed or the process exits.
func acquireFileLock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
