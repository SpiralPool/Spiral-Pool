// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package scheduler

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/stratum"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// On the smart port, the coinbase a miner is sent must pay exactly the wallet its
// shares are then validated against (handleShare rewrites MinerAddress), or every
// share fails validation.
func TestMultiPort_NotifyPaysWalletSharesAreValidatedAgainst(t *testing.T) {
	const prefix, suffix = "ffffffff0100f2052a01000000", "00000000"
	scripts := map[string][]byte{
		"mock_DGB_address": {0xcc}, // pool's configured DGB wallet
		"DMappedWallet":    {0xdd}, // wallet_map entry
		"DMinerOwnAddress": {0xee}, // miner's own username — never paid on the smart port
	}

	tests := []struct {
		name      string
		walletMap map[string]map[string]string
		username  string
		wantPays  string
	}{
		{"configured wallet", nil, "DMinerOwnAddress", "01cc"},
		{"wallet map", map[string]map[string]string{"rig1": {"DGB": "DMappedWallet"}}, "rig1", "01dd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
			job := &protocol.Job{
				ID:              "dg000001",
				PrevBlockHash:   "000000000000000000000000000000000000000000000000000000000000beef",
				CoinBase1:       "01000000010000",
				CoinBase2:       prefix + "0100" + suffix,
				CoinBase2Prefix: prefix,
				CoinBase2Suffix: suffix,
				PayoutScript:    func(address string) []byte { return scripts[address] },
				MerkleBranches:  []string{},
				Version:         "20000000",
				NBits:           "1d00ffff",
				NTime:           "64000000",
				CreatedAt:       time.Now(),
			}
			pool.currentJob.Store(job)

			ms := NewMultiServer(MultiServerConfig{WalletMap: tt.walletMap, Logger: zap.NewNop()}, nil, nil)
			ms.server = stratum.NewServer(&config.StratumConfig{
				Listen:     "0.0.0.0:0",
				Difficulty: config.DifficultyConfig{Initial: 1},
			}, zap.NewNop())
			ms.RegisterCoinPool(pool)

			client, serverConn := net.Pipe()
			t.Cleanup(func() {
				client.Close()
				serverConn.Close()
			})
			notified := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(client)
				for scanner.Scan() {
					var msg struct {
						Method string        `json:"method"`
						Params []interface{} `json:"params"`
					}
					if json.Unmarshal(scanner.Bytes(), &msg) == nil && msg.Method == "mining.notify" && len(msg.Params) > 3 {
						if coinbase2, ok := msg.Params[3].(string); ok {
							notified <- coinbase2
						}
					}
				}
			}()

			session := &protocol.Session{ID: 7, Conn: serverConn, MinerAddress: tt.username}
			ms.sessionCoin.Store(session.ID, "DGB")
			ms.sendCoinJob(session, "DGB", false)

			var sent string
			select {
			case sent = <-notified:
			case <-time.After(2 * time.Second):
				t.Fatal("miner was not sent a job")
			}

			ms.handleShare(&protocol.Share{SessionID: session.ID, JobID: job.ID, MinerAddress: session.MinerAddress})
			pool.mu.Lock()
			if len(pool.receivedShares) != 1 {
				pool.mu.Unlock()
				t.Fatalf("pool received %d shares, want 1", len(pool.receivedShares))
			}
			validatedAddress := pool.receivedShares[0].MinerAddress
			pool.mu.Unlock()

			if want := job.CoinBase2For(validatedAddress); sent != want {
				t.Errorf("notify coinbase2 %s does not match the coinbase shares are validated with (%s, address %q)",
					sent, want, validatedAddress)
			}
			if want := prefix + tt.wantPays + suffix; sent != want {
				t.Errorf("notify coinbase2 = %s, want %s", sent, want)
			}
		})
	}
}
