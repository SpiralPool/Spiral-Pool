// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// v2testminer is a purpose-built binary for regtest validation of the Stratum V2
// protocol stack. It performs the full V2 mining lifecycle programmatically:
//
//  1. TCP connect → Noise NX handshake (encrypted transport)
//  2. SetupConnection → version/flag negotiation
//  3. OpenStandardMiningChannel → channel + target assignment
//  4. Receive NewMiningJob + SetNewPrevHash → job data
//  5. CPU mine (SHA256d or Scrypt) until share meets target
//  6. SubmitSharesStandard → server validates share
//  7. Exit 0 on accepted share, exit 1 on failure
//
// Usage:
//
//	./v2testminer -port 17335 -wallet <addr> -algo sha256d -timeout 30s
//	./v2testminer -port 17336 -wallet <addr> -algo scrypt -timeout 60s
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	v2 "github.com/spiralpool/stratum/internal/stratum/v2"

	"github.com/spiralpool/stratum/internal/crypto"
)

func main() {
	host := flag.String("host", "localhost", "Server hostname")
	port := flag.Int("port", 0, "V2 stratum port (required)")
	wallet := flag.String("wallet", "", "Wallet address for mining channel (required)")
	worker := flag.String("worker", "v2test", "Worker name")
	algo := flag.String("algo", "sha256d", "Hash algorithm: sha256d or scrypt")
	timeout := flag.Duration("timeout", 60*time.Second, "Max time to mine before giving up")
	verbose := flag.Bool("verbose", false, "Verbose logging")
	flag.Parse()

	if *port == 0 {
		fmt.Fprintln(os.Stderr, "ERROR: -port is required")
		flag.Usage()
		os.Exit(1)
	}
	if *wallet == "" {
		fmt.Fprintln(os.Stderr, "ERROR: -wallet is required")
		flag.Usage()
		os.Exit(1)
	}

	userIdentity := *wallet + "." + *worker
	addr := net.JoinHostPort(*host, fmt.Sprintf("%d", *port))

	logf := func(format string, args ...interface{}) {
		if *verbose {
			fmt.Printf("[v2miner] "+format+"\n", args...)
		}
	}

	// ─── Step 1: TCP Connect ─────────────────────────────────────────────
	logf("Connecting to %s...", addr)
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: connect failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	logf("Connected")

	// ─── Step 2: Noise NX Handshake ──────────────────────────────────────
	logf("Performing Noise NX handshake...")
	noiseConn, serverPubKey, err := v2.ClientHandshake(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Noise handshake failed: %v\n", err)
		os.Exit(1)
	}
	logf("Handshake complete, server pubkey: %s", hex.EncodeToString(serverPubKey[:8]))

	// Set overall deadline on the underlying TCP connection.
	// NoiseConn reads/writes through this same conn, so the deadline applies.
	deadline := time.Now().Add(*timeout)
	conn.SetDeadline(deadline)

	// ─── Step 3: SetupConnection ─────────────────────────────────────────
	logf("Sending SetupConnection...")
	setupMsg, err := v2.EncodeSetupConnection(&v2.SetupConnection{
		Protocol:        0, // Mining Protocol
		MinVersion:      2,
		MaxVersion:      2,
		Flags:           v2.ProtocolFlagRequiresStandardJobs | v2.ProtocolFlagRequiresVersionRolling,
		Endpoint:        *host,
		EndpointPort:    uint16(*port),
		VendorID:        "v2testminer",
		HardwareVersion: "1.0",
		FirmwareVersion: "1.0",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: encode SetupConnection: %v\n", err)
		os.Exit(1)
	}
	if _, err := noiseConn.Write(setupMsg); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: send SetupConnection: %v\n", err)
		os.Exit(1)
	}

	// Read SetupConnectionSuccess
	msgType, payload, err := readMessage(noiseConn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: read SetupConnectionSuccess: %v\n", err)
		os.Exit(1)
	}
	if msgType != v2.MsgSetupConnectionSuccess {
		if msgType == v2.MsgSetupConnectionError {
			errMsg, _ := v2.DecodeSetupConnectionError(payload)
			fmt.Fprintf(os.Stderr, "ERROR: SetupConnectionError code=%s flags=0x%x\n", errMsg.ErrorCode, errMsg.Flags)
		} else {
			fmt.Fprintf(os.Stderr, "ERROR: unexpected message type 0x%02x (expected SetupConnectionSuccess 0x01)\n", msgType)
		}
		os.Exit(1)
	}
	setupResp, err := v2.DecodeSetupConnectionSuccess(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: decode SetupConnectionSuccess: %v\n", err)
		os.Exit(1)
	}
	logf("SetupConnection success: version=%d flags=0x%x", setupResp.UsedVersion, setupResp.Flags)

	// ─── Step 4: OpenStandardMiningChannel ───────────────────────────────
	logf("Opening mining channel for %s...", userIdentity)
	openMsg, err := v2.EncodeOpenStandardMiningChannel(&v2.OpenStandardMiningChannel{
		RequestID:       1,
		UserIdentity:    userIdentity,
		NominalHashRate: 1e6, // 1 MH/s — realistic for single-threaded CPU test miner
		MaxTarget:       v2.NBitsToU256(0x207fffff),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: encode OpenStandardMiningChannel: %v\n", err)
		os.Exit(1)
	}
	if _, err := noiseConn.Write(openMsg); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: send OpenStandardMiningChannel: %v\n", err)
		os.Exit(1)
	}

	// Read response — could be success, error, or interleaved SetTarget/NewMiningJob
	var channelID uint32
	var shareTarget *big.Int
	var gotChannel bool

	// Also need job + prevhash before we can mine
	var job *v2.NewMiningJob
	var prevHash *v2.SetNewPrevHash

	for !gotChannel || job == nil || prevHash == nil {
		msgType, payload, err = readMessage(noiseConn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: read message during channel setup: %v\n", err)
			os.Exit(1)
		}

		switch msgType {
		case v2.MsgOpenStandardMiningChannelSuccess:
			resp, err := v2.DecodeOpenStandardMiningChannelSuccess(payload)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: decode OpenStandardMiningChannelSuccess: %v\n", err)
				os.Exit(1)
			}
			channelID = resp.ChannelID
			shareTarget = v2.U256ToTarget(resp.Target)
			gotChannel = true
			logf("Channel opened: id=%d extranoncePrefix=%s",
				channelID, hex.EncodeToString(resp.ExtranoncePrefix))

		case v2.MsgOpenMiningChannelError:
			errResp, _ := v2.DecodeOpenMiningChannelError(payload)
			fmt.Fprintf(os.Stderr, "ERROR: OpenMiningChannelError code=%s\n", errResp.ErrorCode)
			os.Exit(1)

		case v2.MsgNewMiningJob:
			job, err = v2.DecodeNewMiningJob(payload)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: decode NewMiningJob: %v\n", err)
				os.Exit(1)
			}
			logf("NewMiningJob: id=%d version=0x%08x merkleRoot=%s",
				job.JobID, job.Version, hex.EncodeToString(job.MerkleRoot[:8]))

		case v2.MsgSetNewPrevHash:
			prevHash, err = v2.DecodeSetNewPrevHash(payload)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: decode SetNewPrevHash: %v\n", err)
				os.Exit(1)
			}
			logf("SetNewPrevHash: jobID=%d nBits=0x%08x prevHash=%s",
				prevHash.JobID, prevHash.NBits, hex.EncodeToString(prevHash.PrevHash[:8]))

		case v2.MsgSetTarget:
			st, err := v2.DecodeSetTarget(payload)
			if err != nil {
				logf("Warning: decode SetTarget: %v", err)
			} else {
				logf("SetTarget: channel=%d", st.ChannelID)
				// Use the max target from SetTarget if provided
				// Convert 32-byte target back to nBits is complex; use the channel's targetNBits
			}

		default:
			logf("Ignoring message type 0x%02x during setup", msgType)
		}
	}

	// ─── Step 5: CPU Mine (multi-threaded) ──────────────────────────────
	// Build the 80-byte block header template
	if shareTarget == nil || shareTarget.Sign() == 0 {
		fmt.Fprintf(os.Stderr, "ERROR: invalid share target from channel\n")
		os.Exit(1)
	}

	// Use the job's version, or prevHash's nBits for the header
	version := job.Version
	ntime := prevHash.MinNTime

	header := make([]byte, 80)
	// Version (4 bytes LE)
	binary.LittleEndian.PutUint32(header[0:4], version)
	// PrevHash (32 bytes)
	copy(header[4:36], prevHash.PrevHash[:])
	// MerkleRoot (32 bytes)
	copy(header[36:68], job.MerkleRoot[:])
	// nTime (4 bytes LE)
	binary.LittleEndian.PutUint32(header[68:72], ntime)
	// nBits (4 bytes LE) — use network nBits from prevHash
	binary.LittleEndian.PutUint32(header[72:76], prevHash.NBits)
	// Nonce (4 bytes LE) — filled in mining loop
	// header[76:80] = nonce

	// Pre-compute target as 32-byte big-endian for fast byte comparison
	// (eliminates per-hash big.Int allocation that was the bottleneck)
	targetBE := make([]byte, 32)
	tb := shareTarget.Bytes()
	copy(targetBE[32-len(tb):], tb)

	numWorkers := runtime.NumCPU()
	if numWorkers < 1 {
		numWorkers = 1
	}

	fmt.Printf("Mining: algo=%s job=%d workers=%d ...\n", *algo, job.JobID, numWorkers)

	type shareResult struct {
		nonce uint32
		hash  []byte
	}

	resultCh := make(chan shareResult, 1)
	mineCtx, mineCancel := context.WithDeadline(context.Background(), deadline)
	defer mineCancel()

	var totalHashes atomic.Int64
	mineStart := time.Now()

	for w := 0; w < numWorkers; w++ {
		go func(workerID int) {
			hdr := make([]byte, 80)
			copy(hdr, header)
			var hashBE [32]byte

			startNonce := uint32(workerID)
			step := uint32(numWorkers)
			var localCount int64

			for nonce := startNonce; ; nonce += step {
				localCount++

				// Check for cancellation every ~131K hashes per worker
				if localCount&0x1FFFF == 0 {
					select {
					case <-mineCtx.Done():
						totalHashes.Add(localCount)
						return
					default:
					}
					totalHashes.Add(localCount)
					localCount = 0
				}

				// Write nonce into header
				binary.LittleEndian.PutUint32(hdr[76:80], nonce)

				// Hash (SHA256dBytes returns [32]byte — stays on stack, zero allocation)
				var hash [32]byte
				switch *algo {
				case "scrypt":
					h := crypto.ScryptHash(hdr)
					copy(hash[:], h)
				default:
					hash = crypto.SHA256dBytes(hdr)
				}

				// Reverse hash to big-endian for target comparison
				for i := 0; i < 32; i++ {
					hashBE[i] = hash[31-i]
				}

				// Fast byte comparison: hashBE <= targetBE (MSB first)
				if hashMeetsTarget(hashBE[:], targetBE) {
					totalHashes.Add(localCount)
					select {
					case resultCh <- shareResult{nonce: nonce, hash: hash[:]}:
					default:
					}
					mineCancel()
					return
				}

				// Detect uint32 nonce wrap-around (exhausted our partition)
				if nonce+step < nonce {
					totalHashes.Add(localCount)
					return
				}
			}
		}(w)
	}

	// Progress reporter (verbose mode only)
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-mineCtx.Done():
				return
			case <-tick.C:
				total := totalHashes.Load()
				elapsed := time.Since(mineStart).Seconds()
				if elapsed > 0 {
					fmt.Printf("[v2miner] %d hashes (%.1f MH/s)...\n", total, float64(total)/elapsed/1e6)
				}
			}
		}
	}()

	// Wait for result or timeout
	var foundNonce uint32
	var foundHash []byte

	select {
	case result := <-resultCh:
		foundNonce = result.nonce
		foundHash = result.hash
	case <-mineCtx.Done():
		hashCount := totalHashes.Load()
		elapsed := time.Since(mineStart)
		rate := float64(0)
		if elapsed.Seconds() > 0 {
			rate = float64(hashCount) / elapsed.Seconds() / 1e6
		}
		fmt.Fprintf(os.Stderr, "ERROR: mining timeout after %d hashes (%.1f MH/s) in %v\n",
			hashCount, rate, elapsed)
		os.Exit(1)
	}

	hashCount := totalHashes.Load()
	elapsed := time.Since(mineStart)
	rate := float64(0)
	if elapsed.Seconds() > 0 {
		rate = float64(hashCount) / elapsed.Seconds() / 1e6
	}
	fmt.Printf("Share found: nonce=%d hash=%s (%d hashes in %v, %.1f MH/s)\n",
		foundNonce, hex.EncodeToString(foundHash), hashCount, elapsed.Round(time.Millisecond), rate)

	// Reset TCP deadline for share submission (mining may have consumed most of the original timeout)
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	// ─── Step 6: Submit Share ────────────────────────────────────────────
	logf("Submitting share...")
	submitMsg := v2.EncodeSubmitSharesStandard(&v2.SubmitSharesStandard{
		ChannelID:   channelID,
		SequenceNum: 0,
		JobID:       job.JobID,
		Nonce:       foundNonce,
		NTime:       ntime,
		Version:     version,
	})
	if _, err := noiseConn.Write(submitMsg); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: send SubmitSharesStandard: %v\n", err)
		os.Exit(1)
	}

	// ─── Step 7: Read Response ───────────────────────────────────────────
	// The server may send additional jobs/prevhash messages before the share result.
	// Keep reading until we get the submit response.
	for {
		msgType, payload, err = readMessage(noiseConn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: read share response: %v\n", err)
			os.Exit(1)
		}

		switch msgType {
		case v2.MsgSubmitSharesSuccess:
			resp, err := v2.DecodeSubmitSharesSuccess(payload)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: decode SubmitSharesSuccess: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("SHARE ACCEPTED: channel=%d seq=%d count=%d sum=%d\n",
				resp.ChannelID, resp.LastSequenceNum, resp.NewSubmissionsCount, resp.NewSharesSum)
			os.Exit(0) // SUCCESS

		case v2.MsgSubmitSharesError:
			resp, err := v2.DecodeSubmitSharesError(payload)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: decode SubmitSharesError: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "SHARE REJECTED: channel=%d seq=%d code=%s\n",
					resp.ChannelID, resp.SequenceNum, resp.ErrorCode)
			}
			os.Exit(1) // FAILURE

		case v2.MsgNewMiningJob, v2.MsgSetNewPrevHash, v2.MsgSetTarget:
			logf("Ignoring interleaved message 0x%02x while waiting for share response", msgType)
			continue

		default:
			logf("Ignoring unexpected message 0x%02x", msgType)
			continue
		}
	}
}

// readMessage reads a single SV2 message (header + payload) from an encrypted connection.
func readMessage(conn io.Reader) (uint8, []byte, error) {
	// Read 6-byte header
	headerBuf := make([]byte, v2.HeaderSize)
	if _, err := io.ReadFull(conn, headerBuf); err != nil {
		return 0, nil, fmt.Errorf("read header: %w", err)
	}

	var hdr v2.MessageHeader
	hdr.ExtensionType = binary.LittleEndian.Uint16(headerBuf[0:2])
	hdr.MsgType = headerBuf[2]
	hdr.Length = uint32(headerBuf[3]) | uint32(headerBuf[4])<<8 | uint32(headerBuf[5])<<16

	if hdr.Length > v2.MaxMessageSize {
		return 0, nil, fmt.Errorf("message too large: %d bytes", hdr.Length)
	}

	// Read payload
	payload := make([]byte, hdr.Length)
	if hdr.Length > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return 0, nil, fmt.Errorf("read payload (%d bytes): %w", hdr.Length, err)
		}
	}

	return hdr.MsgType, payload, nil
}

// hashMeetsTarget returns true if hash <= target (both 32-byte big-endian).
// This is the hot-path comparison — zero allocation, pure byte comparison.
func hashMeetsTarget(hash, target []byte) bool {
	for i := 0; i < 32; i++ {
		if hash[i] < target[i] {
			return true
		}
		if hash[i] > target[i] {
			return false
		}
	}
	return true // exactly equal
}
