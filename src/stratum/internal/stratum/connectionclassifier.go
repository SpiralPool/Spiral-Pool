// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package stratum - Connection Classifier: fingerprints stratum connections as
// ASIC, PROXY (stratum aggregation proxy), or MARKETPLACE (hashrate rental service).
//
// Detection runs in three levels of increasing cost:
//
//	Level 1 — User-agent (immediate): MinerClassFarmProxy and MinerClassHashMarketplace
//	          are already identified by SpiralRouter before the first share arrives.
//	          Classification is instant and final.
//
//	Level 2 — Handshake fingerprint (~100ms): subscribe→authorize timing and worker
//	          name pattern. Physical firmware takes ≥20ms after subscribe to send
//	          authorize because it initialises work queues. Automated software
//	          (proxies, marketplace dispatchers) typically authorizes in <5ms.
//	          Worker names like "user.worker001" or "user.rig-03" are additional signals.
//
//	Level 3 — Share-behaviour entropy (first 50–100 shares): In a proxy, many
//	          downstream miners each occupy a disjoint extranonce2 sub-range.
//	          Shares from a proxy therefore show high per-byte Shannon entropy
//	          across all byte positions. A single ASIC increments sequentially,
//	          producing near-zero entropy in the upper bytes.
//
// Detection never blocks or disconnects miners — it only tags connections for
// observability, analytics, and optional policy decisions.
package stratum

import (
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sync"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// CONNECTION TYPE
// ═══════════════════════════════════════════════════════════════════════════════

// ConnectionType is the detected category of a stratum connection.
type ConnectionType int

const (
	// ConnectionTypeUnknown is the default before enough evidence has accumulated.
	ConnectionTypeUnknown ConnectionType = iota

	// ConnectionTypeASIC is a physical single-device miner (Antminer, Whatsminer,
	// Avalon, BitAxe, etc.). extranonce2 values are sequential and timing is firmware-paced.
	ConnectionTypeASIC

	// ConnectionTypeProxy is a stratum aggregation proxy (e.g. Braiins Farm Proxy).
	// A single upstream connection carries aggregated hashrate from many downstream miners.
	// extranonce2 entropy is high; authorizes instantly.
	ConnectionTypeProxy

	// ConnectionTypeMarketplace is a hashrate rental / marketplace connection
	// (e.g. NiceHash/Excavator, MiningRigRentals). May be an individual rig or routed
	// through the platform's own proxy infrastructure.
	ConnectionTypeMarketplace
)

func (ct ConnectionType) String() string {
	switch ct {
	case ConnectionTypeASIC:
		return "ASIC"
	case ConnectionTypeProxy:
		return "PROXY"
	case ConnectionTypeMarketplace:
		return "MARKETPLACE"
	default:
		return "UNKNOWN"
	}
}

// ConnectionClassification is the result attached to a connection.
type ConnectionClassification struct {
	// Type is the detected connection category.
	Type ConnectionType

	// Confidence is a [0.0, 1.0] score. 0 means unclassified; 1.0 means certain.
	// Level-1 detections start at 0.85–0.95; Level-3 detections reach up to 0.95.
	Confidence float64

	// Signals lists the individual evidence items that contributed to this result,
	// in the order they were observed. Useful for debugging and audit logging.
	Signals []string
}

// ═══════════════════════════════════════════════════════════════════════════════
// CLASSIFIER INTERNALS
// ═══════════════════════════════════════════════════════════════════════════════

// proxyWorkerPattern matches naming conventions common in proxies and marketplaces:
//
//	username.worker001   username.worker-3
//	username.rig1        username.rig-01
//	username.node0       username.slot2
//	username.001         username.0001   (zero-padded pure numeric suffix)
var proxyWorkerPattern = regexp.MustCompile(
	`\.(worker|rig|node|machine|slot|proxy)[-_]?\d+$` + // explicit device keyword
		`|\.0{2,}\d+$`, // zero-padded numeric suffix like .001, .0001 (requires trailing digit)
)

// Thresholds for Level 2 timing analysis.
const (
	authDelayInstantMs = 5  // <5ms: almost certainly automated (proxy/software)
	authDelayFastMs    = 20 // <20ms: likely automated
)

// Thresholds for Level 3 entropy analysis.
const (
	// en2EntropyProxyThresh: average per-byte entropy above this → proxy-like.
	// A uniform-random byte stream has entropy = 8 bits/byte.
	// Proxy streams with N workers covering N sub-ranges typically reach 5–7 bits/byte.
	en2EntropyProxyThresh = 5.0

	// en2EntropyASICThresh: average per-byte entropy below this → sequential ASIC.
	// Upper bytes of a sequential counter stay near 0 → entropy ≈ 0.
	en2EntropyASICThresh = 2.0

	// minSamplesForEntropy: minimum shares before entropy is meaningful.
	minSamplesForEntropy = 10

	// samplesForFinalize: shares after which the entropy classification is treated as final.
	samplesForFinalize = 50

	// maxEn2Samples: hard cap on stored samples per session (memory bound).
	maxEn2Samples = 100
)

// connectionEvidence accumulates per-session signals for classification.
// All writes come from the session's single read goroutine.
// GetClassification may be called from any goroutine, hence mu.
type connectionEvidence struct {
	mu sync.RWMutex

	// Level 1
	minerClass MinerClass

	// Level 2
	subscribeAt time.Time
	authorizeAt time.Time
	workerName  string

	// Level 3
	en2Samples [][]byte    // raw extranonce2 bytes, capped at maxEn2Samples
	shareTimes []time.Time // timestamps of the same shares

	// Current result (may be updated incrementally through the three levels)
	result    ConnectionClassification
	finalized bool // no further updates once true
}

// ═══════════════════════════════════════════════════════════════════════════════
// CONNECTION CLASSIFIER
// ═══════════════════════════════════════════════════════════════════════════════

// ConnectionClassifier tracks per-session evidence and emits ConnectionClassification
// values as evidence accumulates. Safe for concurrent use.
type ConnectionClassifier struct {
	mu       sync.RWMutex
	sessions map[uint64]*connectionEvidence

	cbMu         sync.RWMutex
	onClassified func(sessionID uint64, c ConnectionClassification)
}

// NewConnectionClassifier creates a ready-to-use classifier.
func NewConnectionClassifier() *ConnectionClassifier {
	return &ConnectionClassifier{
		sessions: make(map[uint64]*connectionEvidence),
	}
}

// SetClassifiedHandler registers a callback that fires when a connection is first
// classified or when its classification meaningfully changes (e.g. confidence rises).
// The callback is invoked without any internal locks held, so it is safe to call
// back into the classifier from within it.
func (cc *ConnectionClassifier) SetClassifiedHandler(fn func(sessionID uint64, c ConnectionClassification)) {
	cc.cbMu.Lock()
	cc.onClassified = fn
	cc.cbMu.Unlock()
}

// RecordSubscribe is called immediately after mining.subscribe completes and
// session.UserAgent has been set. This is the Level 1 fast path: FarmProxy and
// HashMarketplace connections are classified and finalized here.
func (cc *ConnectionClassifier) RecordSubscribe(sessionID uint64, class MinerClass, at time.Time) {
	ev := cc.getOrCreate(sessionID)
	ev.mu.Lock()

	ev.minerClass = class
	ev.subscribeAt = at

	var notify ConnectionClassification
	shouldNotify := false

	switch class {
	case MinerClassFarmProxy:
		ev.result = ConnectionClassification{
			Type:       ConnectionTypeProxy,
			Confidence: 0.95,
			Signals:    []string{"user-agent: classified as farm_proxy by SpiralRouter"},
		}
		ev.finalized = true
		notify = ev.result
		shouldNotify = true

	case MinerClassHashMarketplace:
		ev.result = ConnectionClassification{
			Type:       ConnectionTypeMarketplace,
			Confidence: 0.85,
			Signals:    []string{"user-agent: classified as hash_marketplace by SpiralRouter"},
		}
		ev.finalized = true
		notify = ev.result
		shouldNotify = true
	}

	ev.mu.Unlock()

	if shouldNotify {
		cc.notify(sessionID, notify)
	}
}

// RecordAuthorize is called immediately after mining.authorize completes and
// session.WorkerName has been set. Applies Level 2 timing and name heuristics.
func (cc *ConnectionClassifier) RecordAuthorize(sessionID uint64, workerName string, at time.Time) {
	ev := cc.getOrCreate(sessionID)
	ev.mu.Lock()

	ev.workerName = workerName
	ev.authorizeAt = at

	if ev.finalized {
		ev.mu.Unlock()
		return
	}

	prevType := ev.result.Type
	cc.applyLevel2(ev)
	newType := ev.result.Type
	notify := ev.result
	ev.mu.Unlock()

	if newType != ConnectionTypeUnknown && newType != prevType {
		cc.notify(sessionID, notify)
	}
}

// RecordShare is called when a mining.submit arrives. Applies Level 3
// extranonce2 entropy analysis after enough samples accumulate.
// en2hex is the raw hex-encoded extranonce2 string from the submit params.
func (cc *ConnectionClassifier) RecordShare(sessionID uint64, en2hex string, at time.Time) {
	ev := cc.getOrCreate(sessionID)
	ev.mu.Lock()

	if ev.finalized || len(ev.en2Samples) >= maxEn2Samples {
		ev.mu.Unlock()
		return
	}

	en2bytes, err := hex.DecodeString(en2hex)
	if err == nil && len(en2bytes) > 0 {
		ev.en2Samples = append(ev.en2Samples, en2bytes)
		ev.shareTimes = append(ev.shareTimes, at)
	}

	n := len(ev.en2Samples)

	var notify ConnectionClassification
	shouldNotify := false

	// Evaluate at 10, 50, and 100 samples — escalating confidence.
	if n == minSamplesForEntropy || n == samplesForFinalize || n == maxEn2Samples {
		prevType := ev.result.Type
		cc.applyLevel3(ev)
		if ev.result.Type != ConnectionTypeUnknown && ev.result.Type != prevType {
			notify = ev.result
			shouldNotify = true
		}
	}

	ev.mu.Unlock()

	if shouldNotify {
		cc.notify(sessionID, notify)
	}
}

// GetClassification returns the current classification for a session.
// Returns {Type: ConnectionTypeUnknown} if no evidence has been recorded yet.
func (cc *ConnectionClassifier) GetClassification(sessionID uint64) ConnectionClassification {
	cc.mu.RLock()
	ev, ok := cc.sessions[sessionID]
	cc.mu.RUnlock()
	if !ok {
		return ConnectionClassification{Type: ConnectionTypeUnknown}
	}
	ev.mu.RLock()
	c := ev.result
	ev.mu.RUnlock()
	return c
}

// Cleanup removes all evidence for a session. Call on disconnect to avoid leaks.
func (cc *ConnectionClassifier) Cleanup(sessionID uint64) {
	cc.mu.Lock()
	delete(cc.sessions, sessionID)
	cc.mu.Unlock()
}

// ═══════════════════════════════════════════════════════════════════════════════
// INTERNAL — LEVEL 2 ANALYSIS
// ═══════════════════════════════════════════════════════════════════════════════

// applyLevel2 applies subscribe→authorize timing and worker-name heuristics.
// Must be called with ev.mu held for writing.
func (cc *ConnectionClassifier) applyLevel2(ev *connectionEvidence) {
	score := ev.result.Confidence
	signals := append([]string(nil), ev.result.Signals...) // copy

	// ── Timing anomaly ───────────────────────────────────────────────────────
	// NOTE: On local networks (LAN), even physical ASICs (Antminer S19, etc.)
	// can authorize in <5ms because there's no WAN latency. Only apply timing
	// signals when Level 1 did NOT already identify the miner as a known ASIC
	// class (i.e. minerClass is still Unknown). If Level 1 matched a known
	// device via user-agent, timing evidence is redundant and misleading.
	if !ev.subscribeAt.IsZero() && !ev.authorizeAt.IsZero() && ev.minerClass == MinerClassUnknown {
		delay := ev.authorizeAt.Sub(ev.subscribeAt)
		switch {
		case delay < time.Duration(authDelayInstantMs)*time.Millisecond:
			signals = append(signals, fmt.Sprintf(
				"auth_delay=%v (<5ms: possible proxy or fast LAN ASIC)",
				delay.Round(time.Microsecond)))
			score += 0.25 // Reduced from 0.40 — LAN ASICs routinely hit <5ms
		case delay < time.Duration(authDelayFastMs)*time.Millisecond:
			signals = append(signals, fmt.Sprintf(
				"auth_delay=%v (<20ms: likely automated or fast LAN)",
				delay.Round(time.Microsecond)))
			score += 0.15 // Reduced from 0.20
		}
	}

	// ── Worker name pattern ───────────────────────────────────────────────────
	if ev.workerName != "" && proxyWorkerPattern.MatchString(ev.workerName) {
		signals = append(signals, fmt.Sprintf(
			"worker_name=%q matches proxy/marketplace naming pattern",
			ev.workerName))
		score += 0.15
	}

	// Only update result if score actually increased.
	if score > ev.result.Confidence {
		connType := ev.result.Type
		// Require score >= 0.40 to classify as proxy from Level 2 alone.
		// Previously 0.15 — a single fast auth on LAN (+0.25) was enough to
		// misclassify physical ASICs (Antminer S19, etc.) as proxies.
		// Now timing alone (0.25) is insufficient; needs a second signal
		// (e.g., proxy worker name pattern +0.15) to reach the threshold.
		if connType == ConnectionTypeUnknown && score >= 0.40 {
			connType = ConnectionTypeProxy
		}
		ev.result = ConnectionClassification{
			Type:       connType,
			Confidence: clampFloat(score, 0, 0.80), // Level 2 capped at 0.80
			Signals:    signals,
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// INTERNAL — LEVEL 3 ANALYSIS
// ═══════════════════════════════════════════════════════════════════════════════

// applyLevel3 applies extranonce2 entropy and share-rate analysis.
// Must be called with ev.mu held for writing.
func (cc *ConnectionClassifier) applyLevel3(ev *connectionEvidence) {
	n := len(ev.en2Samples)
	if n < minSamplesForEntropy {
		return
	}

	entropy := extranonce2Entropy(ev.en2Samples)

	var shareRate float64
	if len(ev.shareTimes) >= 2 {
		span := ev.shareTimes[len(ev.shareTimes)-1].Sub(ev.shareTimes[0])
		if span > 0 {
			shareRate = float64(len(ev.shareTimes)-1) / span.Seconds()
		}
	}

	score := ev.result.Confidence
	signals := append([]string(nil), ev.result.Signals...)

	// ── Entropy signal ────────────────────────────────────────────────────────
	switch {
	case entropy > en2EntropyProxyThresh:
		signals = append(signals, fmt.Sprintf(
			"en2_entropy=%.2f bits/byte (>%.1f: high randomness, multiple nonce sub-ranges)",
			entropy, en2EntropyProxyThresh))
		score += 0.50

	case entropy > 3.5:
		signals = append(signals, fmt.Sprintf(
			"en2_entropy=%.2f bits/byte (elevated)", entropy))
		score += 0.15

	case entropy < en2EntropyASICThresh && n >= samplesForFinalize:
		signals = append(signals, fmt.Sprintf(
			"en2_entropy=%.2f bits/byte (<%.1f: sequential ASIC counter pattern)",
			entropy, en2EntropyASICThresh))
		// Low entropy is positive evidence of a single ASIC — no score bump for PROXY.
	}

	// ── Share-rate signal ─────────────────────────────────────────────────────
	if shareRate > 5 {
		signals = append(signals, fmt.Sprintf(
			"share_rate=%.1f/s (elevated: suggests aggregated hashrate)",
			shareRate))
		score += 0.15
	}

	// ── Resolve classification ────────────────────────────────────────────────
	connType := ev.result.Type

	if score >= 0.70 {
		if connType == ConnectionTypeUnknown || connType == ConnectionTypeASIC {
			connType = ConnectionTypeProxy
		}
		if n >= samplesForFinalize {
			ev.finalized = true
		}
	} else if entropy < en2EntropyASICThresh && n >= samplesForFinalize {
		connType = ConnectionTypeASIC
		ev.finalized = true
		// Sequential ASIC counter is strong evidence; ensure confidence reflects that.
		if score < 0.70 {
			score = 0.70
		}
	}

	ev.result = ConnectionClassification{
		Type:       connType,
		Confidence: clampFloat(score, 0, 0.95),
		Signals:    signals,
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// HELPERS
// ═══════════════════════════════════════════════════════════════════════════════

func (cc *ConnectionClassifier) getOrCreate(sessionID uint64) *connectionEvidence {
	cc.mu.RLock()
	ev, ok := cc.sessions[sessionID]
	cc.mu.RUnlock()
	if ok {
		return ev
	}
	cc.mu.Lock()
	ev, ok = cc.sessions[sessionID] // double-check under write lock
	if !ok {
		ev = &connectionEvidence{}
		cc.sessions[sessionID] = ev
	}
	cc.mu.Unlock()
	return ev
}

func (cc *ConnectionClassifier) notify(sessionID uint64, c ConnectionClassification) {
	cc.cbMu.RLock()
	fn := cc.onClassified
	cc.cbMu.RUnlock()
	if fn != nil {
		fn(sessionID, c)
	}
}

// extranonce2Entropy computes the average per-byte Shannon entropy across all samples.
//
// For a single ASIC incrementing sequentially from 0:
//
//	Upper bytes stay near 0 → entropy ≈ 0 bits/byte
//	Lower bytes cycle 0–255 with ~N samples → entropy ≈ log2(N) bits/byte
//	Average across 8 bytes is LOW (≈0–2 bits/byte for typical share counts)
//
// For a proxy with W workers, each allocated a disjoint sub-range:
//
//	Upper bytes vary across W workers → high entropy
//	All byte positions show significant variation
//	Average is HIGH (≈5–8 bits/byte with 10+ workers)
//
// Returns 0 if there are fewer than 5 samples (too noisy to be meaningful).
func extranonce2Entropy(samples [][]byte) float64 {
	if len(samples) < 5 {
		return 0
	}

	// Find the shortest extranonce2 across all samples so we compare the same
	// byte positions. Pool sets en2 size uniformly, so this is just a safety guard.
	minLen := len(samples[0])
	for _, s := range samples[1:] {
		if len(s) < minLen {
			minLen = len(s)
		}
	}
	if minLen == 0 {
		return 0
	}

	total := float64(len(samples))
	totalEntropy := 0.0

	for pos := 0; pos < minLen; pos++ {
		var freq [256]int
		for _, s := range samples {
			freq[s[pos]]++
		}
		h := 0.0
		for _, count := range freq {
			if count > 0 {
				p := float64(count) / total
				h -= p * math.Log2(p)
			}
		}
		totalEntropy += h
	}

	return totalEntropy / float64(minLen)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
