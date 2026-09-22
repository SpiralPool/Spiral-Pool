// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/pkg/protocol"
)

var (
	minerAScript = bytes.Repeat([]byte{0xaa}, 22)
	minerBScript = bytes.Repeat([]byte{0xbb}, 22)
)

// perMinerJob is validJob with its coinbase2 split around the pool's P2PKH reward
// script, as the job manager produces for SOLO per-miner payouts.
func perMinerJob() *protocol.Job {
	job := validJob()
	job.CoinBase2Prefix = "ffffffff0100f2052a01000000" // sequence | 1 output | value
	job.CoinBase2Suffix = "00000000"                   // locktime
	job.PayoutScript = func(address string) []byte {
		switch address {
		case "minerA":
			return minerAScript
		case "minerB":
			return minerBScript
		}
		return nil
	}
	return job
}

func shareFrom(address string) *protocol.Share {
	share := validShare()
	share.MinerAddress = address
	return share
}

// Two miners hashing the same job must each commit to their own coinbase —
// the regression was a single shared address slot, so the last miner to
// authorize was paid for every miner's blocks.
func TestPerMinerCoinbase_MerkleRootDiffersPerMiner(t *testing.T) {
	job := perMinerJob()

	rootA, err := computeMerkleRoot(job, shareFrom("minerA"))
	if err != nil {
		t.Fatal(err)
	}
	rootB, err := computeMerkleRoot(job, shareFrom("minerB"))
	if err != nil {
		t.Fatal(err)
	}
	rootPool, err := computeMerkleRoot(job, shareFrom(""))
	if err != nil {
		t.Fatal(err)
	}
	rootInvalid, err := computeMerkleRoot(job, shareFrom("not-an-address"))
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(rootA, rootB) || bytes.Equal(rootA, rootPool) || bytes.Equal(rootB, rootPool) {
		t.Fatal("miners A, B and the pool must commit to different coinbases")
	}
	if !bytes.Equal(rootInvalid, rootPool) {
		t.Error("an invalid payout address must fall back to the pool coinbase")
	}
}

// The block submitted for a share must pay the miner who found it, and its
// coinbase must hash to the merkle root the share was validated against.
func TestPerMinerCoinbase_BlockPaysFindingMiner(t *testing.T) {
	job := perMinerJob()
	share := shareFrom("minerA")

	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatal(err)
	}
	blockHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatal(err)
	}
	block, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatal(err)
	}

	poolScript, _ := hex.DecodeString("76a914000000000000000000000000000000000000000088ac")
	if !bytes.Contains(block, minerAScript) {
		t.Error("block does not pay the finding miner")
	}
	if bytes.Contains(block, minerBScript) || bytes.Contains(block, poolScript) {
		t.Error("block pays someone other than the finding miner")
	}

	// Coinbase is the only transaction: header(80) | tx count(1) | coinbase.
	coinbase := block[81:]
	if !bytes.Equal(crypto.SHA256d(coinbase), header[36:68]) {
		t.Error("submitted coinbase does not match the header merkle root")
	}

	rebuilt, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt != blockHex {
		t.Error("recovery rebuild must produce the same per-miner block")
	}
}
