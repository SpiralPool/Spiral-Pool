// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build windows

package cmd

// matchOwner is a no-op on Windows, where files carry no Unix owner.
func matchOwner(ref string, paths []string) error {
	return nil
}
