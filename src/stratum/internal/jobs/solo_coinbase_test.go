// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// testLogger returns a no-op logger for tests to avoid nil pointer panics
func testLogger() *zap.SugaredLogger {
	logger, _ := zap.NewDevelopment()
	return logger.Sugar()
}

const (
	soloPoolAddress = "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"
	soloMinerA      = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	soloMinerB      = "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy"
)

// newSoloTestManager builds a Manager able to run generateJob without a daemon,
// with per-miner payout switched ON. Most tests here are about that feature; the
// shipped default is off and is covered by newSoloTestManagerPayoutOff.
func newSoloTestManager(t *testing.T, symbol, poolAddress string) *Manager {
	m := newSoloTestManagerPayoutOff(t, symbol, poolAddress)
	m.stratumCfg.PayoutFromWorkerName = true
	return m
}

// newSoloTestManagerPayoutOff builds the same Manager with the shipped default:
// every block pays the configured wallet, whatever a miner calls itself.
func newSoloTestManagerPayoutOff(t *testing.T, symbol, poolAddress string) *Manager {
	t.Helper()
	coinImpl, err := coin.Create(symbol)
	if err != nil {
		t.Fatalf("coin.Create(%s): %v", symbol, err)
	}
	return &Manager{
		coinImpl:     coinImpl,
		outputScript: mustPayoutScript(t, coinImpl, poolAddress),
		coinbaseText: "/SpiralPool/",
		stratumCfg:   &config.StratumConfig{},
		jobIDPrefix:  "t0",
		logger:       zap.NewNop().Sugar(),
	}
}

func mustPayoutScript(t *testing.T, coinImpl coin.Coin, address string) []byte {
	t.Helper()
	script, err := coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{PoolAddress: address})
	if err != nil {
		t.Fatalf("BuildCoinbaseScript(%s): %v", address, err)
	}
	return script
}

func soloTemplate() *daemon.BlockTemplate {
	return &daemon.BlockTemplate{
		Version:           0x20000000,
		PreviousBlockHash: strings.Repeat("00", 31) + "01",
		Bits:              "1d00ffff",
		CurTime:           1700000000,
		Height:            100001,
		CoinbaseValue:     625000000,
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("invalid hex %q: %v", s, err)
	}
	return b
}

// TestSoloMinerAddress_ValidAddress tests that valid miner addresses are accepted.
// Note: Uses real valid addresses with proper checksums.
func TestSoloMinerAddress_ValidAddress(t *testing.T) {
	tests := []struct {
		name         string
		coinSymbol   string
		minerAddress string
		expectValid  bool
	}{
		// BTC addresses (real valid addresses)
		{"BTC P2PKH", "BTC", "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2", true},
		{"BTC Bech32", "BTC", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", true},

		// LTC addresses (real valid addresses)
		{"LTC P2PKH", "LTC", "LaMT348PWRnrqeeWArpwQPbuanpXDZGEUz", true},
		{"LTC Bech32", "LTC", "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9", true},

		// Invalid addresses
		{"Invalid - empty", "BTC", "", false},
		{"Invalid - too short", "BTC", "1ABC", false},
		{"Invalid - wrong prefix", "LTC", "1WrongPrefix", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create coin implementation
			coinImpl, err := coin.Create(tt.coinSymbol)
			if err != nil {
				t.Fatalf("Failed to create coin %s: %v", tt.coinSymbol, err)
			}

			// Try to build coinbase script (this is what payoutScript does internally)
			if tt.minerAddress != "" {
				_, err = coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
					PoolAddress: tt.minerAddress,
				})
			} else {
				err = nil // Empty address pays the pool address
			}

			isValid := err == nil
			if tt.minerAddress == "" {
				isValid = false // Empty address is a special case (pool fallback)
			}

			if isValid != tt.expectValid {
				if tt.expectValid {
					t.Errorf("Expected valid address but got error: %v", err)
				} else {
					t.Errorf("Expected invalid address but it was accepted")
				}
			}
		})
	}
}

// TestPayoutScript_ValidatesPerCoin verifies the per-address script lookup used
// for every notify and share.
func TestPayoutScript_ValidatesPerCoin(t *testing.T) {
	m := newSoloTestManager(t, "BTC", soloPoolAddress)

	if got, want := m.payoutScript(soloMinerA), mustPayoutScript(t, m.coinImpl, soloMinerA); !bytes.Equal(got, want) {
		t.Errorf("payoutScript(minerA) = %x, want %x", got, want)
	}
	for _, address := range []string{"", "rig1", "INVALID_ADDRESS_123", "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9"} {
		if got := m.payoutScript(address); got != nil {
			t.Errorf("payoutScript(%q) = %x, want nil (pool address fallback)", address, got)
		}
	}
	// Cached results are stable.
	if got := m.payoutScript("INVALID_ADDRESS_123"); got != nil {
		t.Errorf("cached invalid address returned %x", got)
	}
}

// TestPerMinerCoinbase_DisabledByDefault pins the shipped default: with
// payout_from_worker_name unset, a miner that authorizes with a perfectly valid
// address for the coin is still paid to the configured wallet. The pool is for a
// single operator on a private network, so a worker name must not be able to
// redirect a block reward unless the operator has explicitly allowed it.
func TestPerMinerCoinbase_DisabledByDefault(t *testing.T) {
	m := newSoloTestManagerPayoutOff(t, "BTC", soloPoolAddress)
	if m.stratumCfg.PayoutFromWorkerName {
		t.Fatal("test setup: expected the default (worker-name payout off)")
	}

	job, err := m.generateJob(context.Background(), soloTemplate(), true)
	if err != nil {
		t.Fatalf("generateJob: %v", err)
	}

	if job.PayoutScript != nil {
		t.Error("job carries a per-miner payout builder with the setting off")
	}

	// Every miner, valid address or not, gets the pool's own coinbase2.
	for _, address := range []string{soloMinerA, soloMinerB, "rig1", ""} {
		if got := job.CoinBase2For(address); got != job.CoinBase2 {
			t.Errorf("CoinBase2For(%q) = %s, want the configured wallet's coinbase2 %s",
				address, got, job.CoinBase2)
		}
	}

	// The pool address must still be the one paid in the coinbase itself.
	if !strings.Contains(job.CoinBase2, hex.EncodeToString(m.outputScript)) {
		t.Error("coinbase2 does not pay the configured wallet")
	}
}

// TestPerMinerCoinbase_EachMinerPaysOwnAddress is the regression test for the
// shared reward address: the job manager held ONE miner address per coin, set by
// whichever miner authorized last, so that miner was paid for every connected
// miner's blocks. Each miner's coinbase must now pay its own address, for every
// coinbase shape, regardless of the order miners are served.
func TestPerMinerCoinbase_EachMinerPaysOwnAddress(t *testing.T) {
	witness := "6a24aa21a9ed" + strings.Repeat("ab", 32)
	shapes := map[string]func(*daemon.BlockTemplate){
		"plain":   func(*daemon.BlockTemplate) {},
		"witness": func(tmpl *daemon.BlockTemplate) { tmpl.DefaultWitnessCommitment = witness },
		"witness + oracle": func(tmpl *daemon.BlockTemplate) {
			tmpl.DefaultWitnessCommitment = witness
			tmpl.DefaultOracleCommitment = "6a0401020304"
		},
	}

	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			m := newSoloTestManager(t, "BTC", soloPoolAddress)
			tmpl := soloTemplate()
			shape(tmpl)

			job, err := m.generateJob(context.Background(), tmpl, true)
			if err != nil {
				t.Fatalf("generateJob: %v", err)
			}
			if job.PayoutScript == nil {
				t.Fatal("job has no per-miner payout split")
			}

			poolScript := m.outputScript
			scriptA := mustPayoutScript(t, m.coinImpl, soloMinerA)
			scriptB := mustPayoutScript(t, m.coinImpl, soloMinerB)

			cb2A := job.CoinBase2For(soloMinerA)
			cb2B := job.CoinBase2For(soloMinerB)
			if again := job.CoinBase2For(soloMinerA); again != cb2A {
				t.Error("miner A's coinbase changed after serving miner B")
			}

			for _, tc := range []struct {
				who        string
				cb2        []byte
				pays       []byte
				mustNotPay [][]byte
			}{
				{"miner A", mustDecode(t, cb2A), scriptA, [][]byte{scriptB, poolScript}},
				{"miner B", mustDecode(t, cb2B), scriptB, [][]byte{scriptA, poolScript}},
			} {
				if !bytes.Contains(tc.cb2, tc.pays) {
					t.Errorf("%s coinbase does not pay its own address", tc.who)
				}
				for _, other := range tc.mustNotPay {
					if bytes.Contains(tc.cb2, other) {
						t.Errorf("%s coinbase pays another address", tc.who)
					}
				}
			}

			// Everything except the reward script is identical to the pool coinbase.
			if job.CoinBase2For(soloPoolAddress) != job.CoinBase2 {
				t.Error("rebuilding with the pool address must reproduce CoinBase2 exactly")
			}
		})
	}
}

// Miners whose username is not a valid address for the coin keep paying the pool
// address, matching the previous fallback.
func TestPerMinerCoinbase_InvalidAddressPaysPool(t *testing.T) {
	m := newSoloTestManager(t, "BTC", soloPoolAddress)
	job, err := m.generateJob(context.Background(), soloTemplate(), true)
	if err != nil {
		t.Fatalf("generateJob: %v", err)
	}
	for _, address := range []string{"", "rig1", "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9"} {
		if got := job.CoinBase2For(address); got != job.CoinBase2 {
			t.Errorf("CoinBase2For(%q) = %s, want pool coinbase %s", address, got, job.CoinBase2)
		}
	}
}

// Both coinbase builders (plain and merge-mining) must split cleanly.
func TestSplitCoinbase2_AllBuilders(t *testing.T) {
	m := newTestJobManager()
	tmpl := &daemon.BlockTemplate{
		Height:                   100001,
		CoinbaseValue:            625000000,
		Bits:                     "1d00ffff",
		DefaultWitnessCommitment: "6a24aa21a9ed" + strings.Repeat("ab", 32),
	}
	_, cb2 := m.buildCoinbase(tmpl)
	_, cb2Aux := m.buildCoinbase2Only(tmpl)

	for name, coinbase2 := range map[string][]byte{"buildCoinbase": cb2, "buildCoinbase2Only": cb2Aux} {
		prefix, suffix, ok := splitCoinbase2(coinbase2, m.outputScript)
		if !ok {
			t.Fatalf("%s: split failed", name)
		}
		rebuilt := append(append(append([]byte{}, prefix...), crypto.EncodeVarInt(uint64(len(m.outputScript)))...), m.outputScript...)
		rebuilt = append(rebuilt, suffix...)
		if !bytes.Equal(rebuilt, coinbase2) {
			t.Errorf("%s: prefix+script+suffix does not reproduce coinbase2", name)
		}
		if _, _, ok := splitCoinbase2(coinbase2, []byte{0x51}); ok {
			t.Errorf("%s: split must fail for a script not in the coinbase", name)
		}
	}
}

// Usernames are miner-controlled; the address cache must stay bounded.
func TestPayoutScript_CacheBounded(t *testing.T) {
	m := newSoloTestManager(t, "BTC", soloPoolAddress)
	for i := 0; i < maxPayoutScriptCache+100; i++ {
		m.payoutScript(fmt.Sprintf("junk-%d", i))
	}
	if n := m.payoutScriptCount.Load(); n > maxPayoutScriptCache {
		t.Errorf("cache holds %d entries, limit %d", n, maxPayoutScriptCache)
	}
}

// TestSoloMinerAddress_StratumUsernameParsing tests that stratum usernames
// are correctly parsed into wallet addresses.
func TestSoloMinerAddress_StratumUsernameParsing(t *testing.T) {
	tests := []struct {
		username       string
		expectedAddr   string
		expectedWorker string
	}{
		// Standard format: address.worker
		{"DWallet123.worker1", "DWallet123", "worker1"},
		{"DWallet123.rig1.gpu0", "DWallet123.rig1", "gpu0"},

		// Just address (no worker)
		{"DWallet123", "DWallet123", "default"},

		// Edge cases
		{"address.", "address", ""},
		{".worker", "", "worker"},
		{"", "", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.username, func(t *testing.T) {
			// Use the same parsing logic as the stratum handler
			addr, worker := parseWorkerNameForTest(tt.username)

			if addr != tt.expectedAddr {
				t.Errorf("Address: got %q, want %q", addr, tt.expectedAddr)
			}
			if worker != tt.expectedWorker {
				t.Errorf("Worker: got %q, want %q", worker, tt.expectedWorker)
			}
		})
	}
}

// parseWorkerNameForTest replicates the stratum handler's parseWorkerName
// to verify the parsing logic.
func parseWorkerNameForTest(name string) (address, worker string) {
	if name == "" {
		return "", "default"
	}
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i], name[i+1:]
		}
	}
	return name, "default"
}

// TestSoloMinerAddress_CrossCoinRejection verifies that a coin rejects
// addresses from other coins (e.g., BTC rejects LTC addresses).
func TestSoloMinerAddress_CrossCoinRejection(t *testing.T) {
	crossCoinTests := []struct {
		coin        string
		wrongAddr   string
		description string
	}{
		{"BTC", "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9", "LTC bech32 on BTC"},
		{"LTC", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "BTC bech32 on LTC"},
	}

	for _, tc := range crossCoinTests {
		t.Run(tc.description, func(t *testing.T) {
			coinImpl, err := coin.Create(tc.coin)
			if err != nil {
				t.Skipf("Coin %s not available: %v", tc.coin, err)
			}

			// Try to build script with wrong coin's address
			_, err = coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
				PoolAddress: tc.wrongAddr,
			})

			if err == nil {
				t.Errorf("%s should reject %s, but it was accepted",
					tc.coin, tc.description)
			}
		})
	}
}
