// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Shared helpers for chaos/stress tests in the shares package.
package shares

import (
	"fmt"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// chaosTestShare creates a deterministic test share for chaos tests.
func chaosTestShare(idx int) *protocol.Share {
	return &protocol.Share{
		MinerAddress:  "DTest1234567890abcdef",
		WorkerName:    fmt.Sprintf("chaos-worker-%d", idx),
		JobID:         fmt.Sprintf("chaos-job-%d", idx%10),
		ExtraNonce1:   fmt.Sprintf("%08x", idx),
		ExtraNonce2:   fmt.Sprintf("%08x", idx*7+1),
		NTime:         "65a8b1c0",
		Nonce:         fmt.Sprintf("%08x", idx*13+3),
		Difficulty:    1.0,
		MinDifficulty: 0.5,
		SubmittedAt:   time.Now(),
	}
}
