// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// matchOwner gives paths the owner and group of ref.
func matchOwner(ref string, paths []string) error {
	info, err := os.Stat(ref)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	for _, path := range paths {
		if err := os.Lchown(path, int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	return nil
}
