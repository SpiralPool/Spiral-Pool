// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build linux || darwin || freebsd || openbsd || netbsd
// +build linux darwin freebsd openbsd netbsd

package pool

import (
	"golang.org/x/sys/unix"
)

// checkDiskSpaceAvailable returns the available disk space in bytes for the given path.
// This is the Unix/Linux implementation using statfs.
func checkDiskSpaceAvailable(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}

	// Bavail = blocks available to unprivileged users
	// Bsize = block size in bytes
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
