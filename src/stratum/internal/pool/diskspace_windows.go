// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build windows
// +build windows

package pool

import (
	"golang.org/x/sys/windows"
)

// checkDiskSpaceAvailable returns the available disk space in bytes for the given path.
// This is the Windows implementation using GetDiskFreeSpaceEx.
func checkDiskSpaceAvailable(path string) (uint64, error) {
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64

	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

	err = windows.GetDiskFreeSpaceEx(
		pathPtr,
		&freeBytesAvailable,
		&totalBytes,
		&totalFreeBytes,
	)
	if err != nil {
		return 0, err
	}

	return freeBytesAvailable, nil
}
