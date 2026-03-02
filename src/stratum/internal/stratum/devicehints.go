// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package stratum - Device Hints Registry for IP-based miner classification
//
// This module allows external systems (like Spiral Sentinel) to provide device
// information for miners that don't send identifiable user-agent strings.
// When a miner connects, the pool can look up its IP in the registry to get
// the correct difficulty class based on HTTP API discovery.
//
// Example flow:
//  1. Sentinel scans network, discovers NMAxe at 192.168.1.15 via HTTP API
//  2. Sentinel pushes device info to pool: POST /api/admin/device-hints
//  3. Miner connects to pool with user-agent "ESP-Miner/2.9.21" (unidentifiable)
//  4. Pool looks up 192.168.1.15 in device hints registry
//  5. Pool finds NMAxe -> MinerClassLow -> difficulty 500
package stratum

import (
	"net"
	"strings"
	"sync"
	"time"
)

// DeviceHint contains device information discovered via HTTP API.
type DeviceHint struct {
	IP          string     `json:"ip"`
	Hostname    string     `json:"hostname,omitempty"` // Device hostname (from mDNS, reverse DNS, or device API)
	DeviceModel string     `json:"deviceModel"`        // e.g., "NMAxe", "NerdQAxe++", "BitAxe Ultra"
	ASICModel   string     `json:"asicModel"`          // e.g., "BM1366", "BM1370"
	ASICCount   int        `json:"asicCount"`          // Number of ASIC chips
	HashrateGHs float64    `json:"hashrateGHs"`        // Observed hashrate in GH/s
	Class       MinerClass `json:"class"`              // Computed miner class
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// DisplayName returns the best available identifier for the device.
// Prefers hostname if available, falls back to IP.
func (h *DeviceHint) DisplayName() string {
	if h.Hostname != "" {
		return h.Hostname
	}
	return h.IP
}

// DeviceHintsRegistry stores device hints indexed by IP address.
// Thread-safe for concurrent access.
type DeviceHintsRegistry struct {
	mu    sync.RWMutex
	hints map[string]*DeviceHint // IP (without port) -> hint
	ttl   time.Duration          // How long hints remain valid
}

// NewDeviceHintsRegistry creates a new registry with the given TTL.
func NewDeviceHintsRegistry(ttl time.Duration) *DeviceHintsRegistry {
	if ttl <= 0 {
		ttl = 24 * time.Hour // Default: hints valid for 24 hours
	}
	return &DeviceHintsRegistry{
		hints: make(map[string]*DeviceHint),
		ttl:   ttl,
	}
}

// normalizeIP extracts just the IP address (no port).
func normalizeIP(addr string) string {
	// Handle "192.168.1.15:12345" format
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	// Already just an IP or couldn't parse
	return strings.TrimSpace(addr)
}

// maxDeviceHints is the maximum number of device hints to store.
// Prevents unbounded memory growth from bulk API submissions.
const maxDeviceHints = 10000

// Set adds or updates a device hint.
func (r *DeviceHintsRegistry) Set(hint *DeviceHint) {
	if hint == nil || hint.IP == "" {
		return
	}

	ip := normalizeIP(hint.IP)
	hint.IP = ip
	hint.UpdatedAt = time.Now()

	// Compute miner class if not set
	if hint.Class == MinerClassUnknown {
		hint.Class = classifyDevice(hint)
	}

	r.mu.Lock()
	// Allow updates to existing IPs, but reject new IPs at capacity
	if _, exists := r.hints[ip]; !exists && len(r.hints) >= maxDeviceHints {
		r.mu.Unlock()
		return
	}
	r.hints[ip] = hint
	r.mu.Unlock()
}

// Get retrieves a device hint by IP address.
// Returns nil if not found or expired.
func (r *DeviceHintsRegistry) Get(addr string) *DeviceHint {
	ip := normalizeIP(addr)

	r.mu.RLock()
	hint, ok := r.hints[ip]
	r.mu.RUnlock()

	if !ok {
		return nil
	}

	// Check TTL
	if time.Since(hint.UpdatedAt) > r.ttl {
		// Expired - remove it
		r.mu.Lock()
		delete(r.hints, ip)
		r.mu.Unlock()
		return nil
	}

	return hint
}

// Delete removes a device hint.
func (r *DeviceHintsRegistry) Delete(addr string) {
	ip := normalizeIP(addr)

	r.mu.Lock()
	delete(r.hints, ip)
	r.mu.Unlock()
}

// GetAll returns all current hints (for debugging/API).
func (r *DeviceHintsRegistry) GetAll() []*DeviceHint {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*DeviceHint, 0, len(r.hints))
	now := time.Now()
	for _, hint := range r.hints {
		if now.Sub(hint.UpdatedAt) <= r.ttl {
			result = append(result, hint)
		}
	}
	return result
}

// Cleanup removes expired hints.
func (r *DeviceHintsRegistry) Cleanup() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	removed := 0
	for ip, hint := range r.hints {
		if now.Sub(hint.UpdatedAt) > r.ttl {
			delete(r.hints, ip)
			removed++
		}
	}
	return removed
}

// GetAvalonDevices returns all Avalon/Canaan devices from the registry.
// Used for LED celebration when a block is found.
func (r *DeviceHintsRegistry) GetAvalonDevices() []*DeviceHint {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*DeviceHint, 0)
	now := time.Now()
	for _, hint := range r.hints {
		if now.Sub(hint.UpdatedAt) <= r.ttl && hint.Class.IsAvalon() {
			result = append(result, hint)
		}
	}
	return result
}

// classifyDevice determines the miner class based on device info.
// IMPORTANT: This function handles IP-based classification for miners discovered
// by Spiral Sentinel or other discovery tools. It must classify Avalon devices
// explicitly to their proper Avalon-specific classes.
func classifyDevice(hint *DeviceHint) MinerClass {
	model := strings.ToLower(hint.DeviceModel)
	asic := strings.ToLower(hint.ASICModel)

	// ========================================================================
	// AVALON/CANAAN DEVICE CLASSIFICATION
	// ========================================================================
	// All Avalon devices must be classified to Avalon-specific classes.
	// This ensures proper difficulty profiles and vardiff behavior.
	// Order: Most specific matches first, then generic patterns.

	// Avalon Home Series (Mini 3, Q)
	if strings.Contains(model, "avalon") || strings.Contains(model, "canaan") {
		switch {
		// Home products
		case strings.Contains(model, "mini") && strings.Contains(model, "3"):
			return MinerClassAvalonHome // Mini 3: 37.5 TH/s
		case strings.Contains(model, "avalon q") || strings.Contains(model, "canaan q"):
			return MinerClassAvalonHome // Avalon Q: 90 TH/s
		case strings.Contains(model, " q") || strings.HasSuffix(model, "q"):
			return MinerClassAvalonHome

		// Nano series
		case strings.Contains(model, "nano"):
			return MinerClassAvalonNano // 3-7 TH/s

		// A15 series (latest generation)
		case strings.Contains(model, "a15") || strings.Contains(model, "1566") ||
			strings.Contains(model, "15pro") || strings.Contains(model, "15xp") ||
			strings.Contains(model, "15se"):
			return MinerClassAvalonPro // 170-215 TH/s

		// A14 series
		case strings.Contains(model, "a14") || strings.Contains(model, "1466"):
			return MinerClassAvalonPro // 150-170 TH/s

		// A13 series
		case strings.Contains(model, "a13") || strings.Contains(model, "1366") ||
			strings.Contains(model, "1346"):
			return MinerClassAvalonHigh // 104-130 TH/s

		// A12 series
		case strings.Contains(model, "a12") || strings.Contains(model, "1246"):
			return MinerClassAvalonHigh // 85-96 TH/s

		// A11 series
		case strings.Contains(model, "1166") || strings.Contains(model, "1146") ||
			strings.Contains(model, "1126"):
			return MinerClassAvalonMid // 64-81 TH/s

		// A10 series
		case strings.Contains(model, "1066") || strings.Contains(model, "1047") ||
			strings.Contains(model, "1026"):
			return MinerClassAvalonMid // 30-50 TH/s

		// A9 series
		case strings.Contains(model, "921") || strings.Contains(model, "911"):
			return MinerClassAvalonMid // 18-20 TH/s

		// A8 series
		case strings.Contains(model, "851") || strings.Contains(model, "841") ||
			strings.Contains(model, "821"):
			return MinerClassAvalonLegacyMid // 11-15 TH/s

		// A7 series
		case strings.Contains(model, "761") || strings.Contains(model, "741") ||
			strings.Contains(model, "721"):
			return MinerClassAvalonLegacyMid // 6-8 TH/s

		// A6 series
		case strings.Contains(model, "641") || strings.Contains(model, "621") ||
			strings.Contains(model, "avalon 6") || strings.Contains(model, "avalon6"):
			return MinerClassAvalonLegacyLow // 3.5 TH/s

		// A3 series (legacy)
		case strings.Contains(model, "avalon 3") || strings.Contains(model, "avalon3"):
			return MinerClassAvalonLegacyLow // 0.8-1 TH/s

		// Generic Avalon fallback - use hashrate if available
		default:
			if hint.HashrateGHs > 0 {
				return classifyAvalonByHashrate(hint.HashrateGHs)
			}
			return MinerClassAvalonMid // Safe default for unknown Avalon
		}
	}

	// ========================================================================
	// NON-AVALON DEVICE CLASSIFICATION
	// ========================================================================

	// Classification based on device model (most reliable)
	switch {
	// Lottery miners (ESP32-only, no ASIC)
	case strings.Contains(model, "nerdminer"):
		return MinerClassLottery
	case strings.Contains(model, "bitmaker"):
		return MinerClassLottery
	case strings.Contains(model, "esp32"):
		return MinerClassLottery

	// Low-end (~400-600 GH/s single ASIC)
	case strings.Contains(model, "nmaxe"):
		return MinerClassLow
	case strings.Contains(model, "bitaxe ultra"):
		return MinerClassLow
	case strings.Contains(model, "bitaxe supra"):
		return MinerClassLow
	case strings.Contains(model, "bitaxe") && hint.ASICCount <= 1:
		return MinerClassLow

	// Mid-range (~1-10 TH/s, multi-ASIC or newer chips)
	case strings.Contains(model, "nerdqaxe"):
		return MinerClassMid
	case strings.Contains(model, "bitaxe hex"):
		return MinerClassMid
	case strings.Contains(model, "bitaxe gamma"):
		return MinerClassMid

	// High-end (older large ASICs)
	case strings.Contains(model, "antminer s9"):
		return MinerClassHigh
	case strings.Contains(model, "antminer s15"):
		return MinerClassHigh

	// Pro (modern ASICs)
	case strings.Contains(model, "antminer s19"):
		return MinerClassPro
	case strings.Contains(model, "antminer s21"):
		return MinerClassPro
	case strings.Contains(model, "whatsminer m5"):
		return MinerClassPro
	}

	// Classification based on ASIC model + count
	switch {
	case asic == "bm1366" && hint.ASICCount <= 1:
		return MinerClassLow // Single BM1366 = ~500 GH/s
	case asic == "bm1368" && hint.ASICCount <= 1:
		return MinerClassLow // Single BM1368 = ~500 GH/s
	case asic == "bm1370":
		if hint.ASICCount >= 4 {
			return MinerClassMid // 4x BM1370 = ~5 TH/s (NerdQAxe++)
		}
		return MinerClassLow // 1-2x BM1370 = ~1-2 TH/s
	case asic == "bm1397":
		return MinerClassMid // BM1397 used in various configs
	}

	// Classification based on observed hashrate (fallback)
	if hint.HashrateGHs > 0 {
		switch {
		case hint.HashrateGHs < 1: // < 1 GH/s
			return MinerClassLottery
		case hint.HashrateGHs < 1000: // < 1 TH/s
			return MinerClassLow
		case hint.HashrateGHs < 10000: // < 10 TH/s
			return MinerClassMid
		case hint.HashrateGHs < 50000: // < 50 TH/s
			return MinerClassHigh
		default: // >= 50 TH/s
			return MinerClassPro
		}
	}

	return MinerClassUnknown
}

// classifyAvalonByHashrate determines the Avalon-specific class based on hashrate.
// Used when device model detection isn't specific enough.
func classifyAvalonByHashrate(hashrateGHs float64) MinerClass {
	switch {
	case hashrateGHs < 5000: // < 5 TH/s
		return MinerClassAvalonNano // Nano series or degraded legacy
	case hashrateGHs < 10000: // 5-10 TH/s
		return MinerClassAvalonLegacyMid // Avalon 7/8 series
	case hashrateGHs < 25000: // 10-25 TH/s
		return MinerClassAvalonMid // Avalon 9 series range
	case hashrateGHs < 85000: // 25-85 TH/s
		return MinerClassAvalonMid // Avalon 10/11 series or Home products
	case hashrateGHs < 150000: // 85-150 TH/s
		return MinerClassAvalonHigh // A12/A13 series
	default: // >= 150 TH/s
		return MinerClassAvalonPro // A14/A15 series
	}
}

// Global registry instance (can be replaced in tests)
var globalDeviceHints = NewDeviceHintsRegistry(24 * time.Hour)

// GetGlobalDeviceHints returns the global device hints registry.
func GetGlobalDeviceHints() *DeviceHintsRegistry {
	return globalDeviceHints
}

// SetGlobalDeviceHints replaces the global registry (for testing).
func SetGlobalDeviceHints(registry *DeviceHintsRegistry) {
	globalDeviceHints = registry
}
