// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v1

import (
	"encoding/json"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// mining.notify must carry a coinbase paying the session's own address.
func TestBuildNotify_PayoutAddress(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	const prefix, suffix = "ffffffff0100f2052a01000000", "00000000"
	job := &protocol.Job{
		ID:              "00000001",
		PrevBlockHash:   "000000000000000000000000000000000000000000000000000000000000dead",
		CoinBase1:       "01000000010000",
		CoinBase2:       prefix + "01ee" + suffix,
		CoinBase2Prefix: prefix,
		CoinBase2Suffix: suffix,
		PayoutScript: func(address string) []byte {
			if address == "miner" {
				return []byte{0xcd}
			}
			return nil
		},
		MerkleBranches: []string{},
		Version:        "20000000",
		NBits:          "1a0377ae",
		NTime:          "64000000",
	}

	tests := map[string]string{
		"miner":   prefix + "01cd" + suffix,
		"":        job.CoinBase2,
		"invalid": job.CoinBase2,
	}
	for address, want := range tests {
		msg, err := h.BuildNotify(job, address)
		if err != nil {
			t.Fatalf("BuildNotify(%q): %v", address, err)
		}
		var notification Notification
		if err := json.Unmarshal(msg, &notification); err != nil {
			t.Fatalf("BuildNotify(%q) produced invalid JSON: %v", address, err)
		}
		if got := notification.Params[3]; got != want {
			t.Errorf("BuildNotify(%q) coinbase2 = %v, want %s", address, got, want)
		}
	}
}
