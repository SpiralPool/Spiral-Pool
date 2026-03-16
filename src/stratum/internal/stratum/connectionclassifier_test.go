// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package stratum

import (
	"encoding/binary"
	"encoding/hex"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// HELPERS
// ═══════════════════════════════════════════════════════════════════════════════

// seqEn2 generates n sequential 8-byte extranonce2 hex strings starting from start.
// Simulates an individual ASIC incrementing its counter.
func seqEn2(n int, start uint64) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], start+uint64(i))
		out[i] = hex.EncodeToString(b[:])
	}
	return out
}

// proxyEn2 generates n random 8-byte extranonce2 hex strings.
// Simulates a proxy allocating disjoint sub-ranges to many workers: the values
// appear random from the pool's perspective.
func proxyEn2(n int, rng *rand.Rand) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		var b [8]byte
		rng.Read(b[:])
		out[i] = hex.EncodeToString(b[:])
	}
	return out
}

func recordShares(cc *ConnectionClassifier, sessionID uint64, en2s []string) {
	t := time.Now()
	for i, en2 := range en2s {
		cc.RecordShare(sessionID, en2, t.Add(time.Duration(i)*time.Millisecond))
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// LEVEL 1: USER-AGENT FAST PATH
// ═══════════════════════════════════════════════════════════════════════════════

func TestClassifier_Level1_FarmProxy(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	cc.RecordSubscribe(1, MinerClassFarmProxy, time.Now())

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeProxy {
		t.Errorf("Type = %v, want PROXY", c.Type)
	}
	if c.Confidence < 0.90 {
		t.Errorf("Confidence = %.2f, want ≥0.90", c.Confidence)
	}
	if len(c.Signals) == 0 {
		t.Error("expected at least one signal")
	}
}

func TestClassifier_Level1_HashMarketplace(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	cc.RecordSubscribe(1, MinerClassHashMarketplace, time.Now())

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeMarketplace {
		t.Errorf("Type = %v, want MARKETPLACE", c.Type)
	}
	if c.Confidence < 0.80 {
		t.Errorf("Confidence = %.2f, want ≥0.80", c.Confidence)
	}
}

func TestClassifier_Level1_FinalizedImmediately(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	cc.RecordSubscribe(1, MinerClassFarmProxy, time.Now())

	// Subsequent signals should not change a finalized result.
	subscribeAt := time.Now().Add(-1 * time.Millisecond)
	cc.RecordAuthorize(1, "user.worker001", subscribeAt.Add(2*time.Millisecond))
	rng := rand.New(rand.NewSource(42))
	recordShares(cc, 1, proxyEn2(60, rng))

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeProxy {
		t.Errorf("finalized type changed: got %v, want PROXY", c.Type)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// LEVEL 2: HANDSHAKE TIMING
// ═══════════════════════════════════════════════════════════════════════════════

func TestClassifier_Level2_InstantAuthorize(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	now := time.Now()
	cc.RecordSubscribe(1, MinerClassPro, now)
	// Authorize 2ms after subscribe — automated software.
	cc.RecordAuthorize(1, "user.worker", now.Add(2*time.Millisecond))

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeProxy {
		t.Errorf("Type = %v, want PROXY for <5ms auth delay", c.Type)
	}
	if c.Confidence < 0.35 {
		t.Errorf("Confidence = %.2f, want ≥0.35", c.Confidence)
	}
}

func TestClassifier_Level2_FastAuthorize(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	now := time.Now()
	cc.RecordSubscribe(1, MinerClassPro, now)
	// 12ms — likely automated but less certain than <5ms.
	cc.RecordAuthorize(1, "user.worker", now.Add(12*time.Millisecond))

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeProxy {
		t.Errorf("Type = %v, want PROXY for <20ms auth delay", c.Type)
	}
	if c.Confidence < 0.15 {
		t.Errorf("Confidence = %.2f, want ≥0.15", c.Confidence)
	}
}

func TestClassifier_Level2_SlowAuthorize_NoSignal(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	now := time.Now()
	cc.RecordSubscribe(1, MinerClassPro, now)
	// 500ms — firmware pace, no proxy signal.
	cc.RecordAuthorize(1, "user.worker", now.Add(500*time.Millisecond))

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeUnknown {
		t.Errorf("Type = %v, want UNKNOWN for slow auth", c.Type)
	}
}

func TestClassifier_Level2_ProxyWorkerName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		worker  string
		wantHit bool
	}{
		{"worker001", "user.worker001", true},
		{"rig-03", "pool.rig-03", true},
		{"node2", "farm.node2", true},
		{"slot0", "farm.slot0", true},
		{"zero-padded", "user.001", true},
		{"zero-padded-long", "user.0001", true},
		{"plain-name", "user.myrig", false},
		{"no-dot", "userworker001", false},
		{"one-digit", "user.1", false}, // single digit is not a proxy pattern
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cc := NewConnectionClassifier()
			now := time.Now()
			cc.RecordSubscribe(1, MinerClassPro, now)
			// Use 500ms auth delay so timing doesn't add score.
			cc.RecordAuthorize(1, tc.worker, now.Add(500*time.Millisecond))

			c := cc.GetClassification(1)
			hasSignal := c.Confidence > 0
			if hasSignal != tc.wantHit {
				t.Errorf("worker=%q: got signal=%v (confidence=%.2f), want signal=%v",
					tc.worker, hasSignal, c.Confidence, tc.wantHit)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// LEVEL 3: EXTRANONCE2 ENTROPY
// ═══════════════════════════════════════════════════════════════════════════════

func TestClassifier_Level3_HighEntropy_IsProxy(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	now := time.Now()
	cc.RecordSubscribe(1, MinerClassPro, now)
	cc.RecordAuthorize(1, "user.worker", now.Add(500*time.Millisecond))

	rng := rand.New(rand.NewSource(7))
	recordShares(cc, 1, proxyEn2(55, rng))

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeProxy {
		t.Errorf("Type = %v, want PROXY for random extranonce2", c.Type)
	}
	if c.Confidence < 0.70 {
		t.Errorf("Confidence = %.2f, want ≥0.70", c.Confidence)
	}
}

func TestClassifier_Level3_LowEntropy_IsASIC(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	now := time.Now()
	cc.RecordSubscribe(1, MinerClassPro, now)
	cc.RecordAuthorize(1, "user.myrig", now.Add(200*time.Millisecond))

	// Sequential counter starting at 0 — classic single-ASIC pattern.
	recordShares(cc, 1, seqEn2(60, 0))

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeASIC {
		t.Errorf("Type = %v, want ASIC for sequential extranonce2", c.Type)
	}
	if c.Confidence < 0.70 {
		t.Errorf("Confidence = %.2f, want ≥0.70", c.Confidence)
	}
}

func TestClassifier_Level3_NotEnoughSamples_NoDecision(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	now := time.Now()
	cc.RecordSubscribe(1, MinerClassPro, now)
	cc.RecordAuthorize(1, "user.myrig", now.Add(200*time.Millisecond))

	// Only 4 shares — below minSamplesForEntropy (10).
	recordShares(cc, 1, seqEn2(4, 0))

	c := cc.GetClassification(1)
	// With only 4 shares, sequential data can't yet finalize as ASIC.
	// Type should remain UNKNOWN (no finalized classification yet).
	if c.Type == ConnectionTypeASIC {
		t.Errorf("Type = ASIC with only 4 shares — too few samples for finalization")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// COMBINED: LEVEL 2 + LEVEL 3 REINFORCE EACH OTHER
// ═══════════════════════════════════════════════════════════════════════════════

func TestClassifier_Combined_Level2And3_HighConfidence(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	now := time.Now()
	cc.RecordSubscribe(1, MinerClassPro, now)
	// Fast auth + proxy worker name → L2 signals proxy.
	cc.RecordAuthorize(1, "pool.worker001", now.Add(3*time.Millisecond))
	// High-entropy en2 → L3 confirms.
	rng := rand.New(rand.NewSource(99))
	recordShares(cc, 1, proxyEn2(55, rng))

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeProxy {
		t.Errorf("Type = %v, want PROXY (combined L2+L3)", c.Type)
	}
	if c.Confidence < 0.80 {
		t.Errorf("Confidence = %.2f, want ≥0.80 for combined signals", c.Confidence)
	}
	if len(c.Signals) < 2 {
		t.Errorf("expected ≥2 signals (timing + entropy), got %d: %v", len(c.Signals), c.Signals)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// CALLBACK
// ═══════════════════════════════════════════════════════════════════════════════

func TestClassifier_Callback_FiredOnClassification(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()

	var mu sync.Mutex
	var called []ConnectionClassification
	cc.SetClassifiedHandler(func(_ uint64, c ConnectionClassification) {
		mu.Lock()
		called = append(called, c)
		mu.Unlock()
	})

	cc.RecordSubscribe(1, MinerClassFarmProxy, time.Now())

	mu.Lock()
	n := len(called)
	mu.Unlock()

	if n == 0 {
		t.Error("callback not fired after Level 1 classification")
	}
	if called[0].Type != ConnectionTypeProxy {
		t.Errorf("callback got Type=%v, want PROXY", called[0].Type)
	}
}

func TestClassifier_Callback_NotFiredForUnknown(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()

	var mu sync.Mutex
	var called []ConnectionClassification
	cc.SetClassifiedHandler(func(_ uint64, c ConnectionClassification) {
		mu.Lock()
		called = append(called, c)
		mu.Unlock()
	})

	// Pro miner, slow auth, only 3 shares — stays UNKNOWN.
	now := time.Now()
	cc.RecordSubscribe(1, MinerClassPro, now)
	cc.RecordAuthorize(1, "user.myrig", now.Add(400*time.Millisecond))
	recordShares(cc, 1, seqEn2(3, 0))

	mu.Lock()
	n := len(called)
	mu.Unlock()

	if n != 0 {
		t.Errorf("callback fired %d time(s) but expected 0 (connection still UNKNOWN)", n)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// LIFECYCLE
// ═══════════════════════════════════════════════════════════════════════════════

func TestClassifier_Cleanup(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	cc.RecordSubscribe(1, MinerClassFarmProxy, time.Now())

	c := cc.GetClassification(1)
	if c.Type != ConnectionTypeProxy {
		t.Fatal("expected PROXY before cleanup")
	}

	cc.Cleanup(1)

	c = cc.GetClassification(1)
	if c.Type != ConnectionTypeUnknown {
		t.Errorf("Type = %v after cleanup, want UNKNOWN", c.Type)
	}
}

func TestClassifier_MultiSession_Isolated(t *testing.T) {
	t.Parallel()
	cc := NewConnectionClassifier()
	now := time.Now()

	// Session 1: Farm Proxy → PROXY
	cc.RecordSubscribe(1, MinerClassFarmProxy, now)

	// Session 2: regular ASIC, slow auth, sequential shares
	cc.RecordSubscribe(2, MinerClassPro, now)
	cc.RecordAuthorize(2, "user.miner", now.Add(500*time.Millisecond))
	recordShares(cc, 2, seqEn2(60, 0))

	c1 := cc.GetClassification(1)
	c2 := cc.GetClassification(2)

	if c1.Type != ConnectionTypeProxy {
		t.Errorf("session 1: Type = %v, want PROXY", c1.Type)
	}
	if c2.Type != ConnectionTypeASIC {
		t.Errorf("session 2: Type = %v, want ASIC", c2.Type)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ENTROPY UNIT TESTS
// ═══════════════════════════════════════════════════════════════════════════════

func TestExtranonce2Entropy_Sequential(t *testing.T) {
	t.Parallel()
	// Sequential 8-byte values: upper bytes all zero, lower bytes 0–49.
	// Upper bytes have zero entropy; lower bytes have ~log2(50)≈5.6 bits.
	// Average across 8 bytes should be LOW.
	en2s := make([][]byte, 50)
	for i := range en2s {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(i))
		en2s[i] = b[:]
	}
	entropy := extranonce2Entropy(en2s)
	if entropy > en2EntropyASICThresh+1.0 {
		t.Errorf("sequential entropy = %.2f, want < %.1f", entropy, en2EntropyASICThresh+1.0)
	}
	t.Logf("sequential entropy = %.3f bits/byte", entropy)
}

func TestExtranonce2Entropy_Random(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(12345))
	en2s := make([][]byte, 50)
	for i := range en2s {
		b := make([]byte, 8)
		rng.Read(b)
		en2s[i] = b
	}
	entropy := extranonce2Entropy(en2s)
	if entropy < en2EntropyProxyThresh {
		t.Errorf("random entropy = %.2f, want > %.1f", entropy, en2EntropyProxyThresh)
	}
	t.Logf("random entropy = %.3f bits/byte", entropy)
}

func TestExtranonce2Entropy_TooFewSamples(t *testing.T) {
	t.Parallel()
	en2s := make([][]byte, 3)
	for i := range en2s {
		en2s[i] = []byte{byte(i), 0, 0, 0, 0, 0, 0, 0}
	}
	entropy := extranonce2Entropy(en2s)
	if entropy != 0 {
		t.Errorf("expected 0 for <5 samples, got %.3f", entropy)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ConnectionType.String()
// ═══════════════════════════════════════════════════════════════════════════════

func TestConnectionType_String(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ct   ConnectionType
		want string
	}{
		{ConnectionTypeUnknown, "UNKNOWN"},
		{ConnectionTypeASIC, "ASIC"},
		{ConnectionTypeProxy, "PROXY"},
		{ConnectionTypeMarketplace, "MARKETPLACE"},
	}
	for _, tc := range cases {
		if got := tc.ct.String(); got != tc.want {
			t.Errorf("ConnectionType(%d).String() = %q, want %q", tc.ct, got, tc.want)
		}
	}
}
