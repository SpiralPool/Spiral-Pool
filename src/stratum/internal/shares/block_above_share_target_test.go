// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// A hash that meets the network target is a block. When a share difficulty is above
// the network difficulty — a low-difficulty chain, regtest, or vardiff climbing past
// the network — such a hash can miss the share target. Both validators checked the
// share target first and rejected the block as low difficulty.
func TestValidators_BlockThatMissesTheShareTargetIsAccepted(t *testing.T) {
	job := &protocol.Job{
		ID:              "lowdiff",
		Version:         "20000000",
		PrevBlockHash:   strings.Repeat("0", 63) + "1",
		CoinBase1:       "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0c03000000",
		CoinBase2:       "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:           "207fffff", // network difficulty far below the share difficulty
		NTime:           "64000000",
		Height:          1,
		CreatedAt:       time.Now(),
		State:           protocol.JobStateActive,
		TransactionData: []string{},
	}
	getJob := func(id string) (*protocol.Job, bool) { return job, id == job.ID }

	// About every other nonce meets this network target.
	blockShare := func() *protocol.Share {
		networkTarget := compactBitsToTarget(job.NBits)
		for n := 0; ; n++ {
			share := &protocol.Share{
				JobID:       job.ID,
				ExtraNonce1: "00000001",
				ExtraNonce2: "00000002",
				NTime:       job.NTime,
				Nonce:       fmt.Sprintf("%08x", n),
				Difficulty:  1e12,
			}
			header, err := buildBlockHeader(job, share)
			if err != nil {
				t.Fatal(err)
			}
			hash := crypto.SHA256d(header)
			if new(big.Int).SetBytes(reverseBytes(hash)).Cmp(networkTarget) <= 0 {
				return share
			}
		}
	}

	check := func(t *testing.T, r *protocol.ShareResult) {
		t.Helper()
		if !r.Accepted || !r.IsBlock {
			t.Fatalf("accepted=%v isBlock=%v reason=%q, want an accepted block", r.Accepted, r.IsBlock, r.RejectReason)
		}
		if r.BlockHex == "" {
			t.Errorf("no block hex to submit (build error %q)", r.BlockBuildError)
		}
	}

	t.Run("Validator", func(t *testing.T) {
		check(t, NewValidator(getJob).Validate(blockShare()))
	})
	t.Run("ValidatorV2", func(t *testing.T) {
		check(t, NewValidatorWithCoin(getJob, coin.MustCreate("BTC")).ValidateWithCoin(blockShare()))
	})
}
