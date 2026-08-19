// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build !race

package shares

// raceEnabled reports whether the binary was built with -race. Tests that assert on
// memory or timing need to account for the detector's overhead.
const raceEnabled = false
