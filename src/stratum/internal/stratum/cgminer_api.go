// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package stratum - CGMiner API client for Avalon LED control
//
// This module provides a simple client to communicate with CGMiner API (port 4028)
// on Avalon miners. Used to trigger LED celebration when the pool finds a block.
package stratum

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// CGMinerAPIPort is the standard CGMiner API port
const CGMinerAPIPort = 4028

// CGMinerClient handles communication with CGMiner API on Avalon miners
type CGMinerClient struct {
	logger *zap.SugaredLogger
}

// NewCGMinerClient creates a new CGMiner API client
func NewCGMinerClient(logger *zap.SugaredLogger) *CGMinerClient {
	return &CGMinerClient{
		logger: logger,
	}
}

// CGMinerCommand represents a command to send to CGMiner API
type CGMinerCommand struct {
	Command   string `json:"command"`
	Parameter string `json:"parameter,omitempty"`
}

// CGMinerResponse represents a response from CGMiner API
type CGMinerResponse struct {
	Status []struct {
		Status      string `json:"STATUS"`
		Code        int    `json:"Code"`
		Msg         string `json:"Msg"`
		Description string `json:"Description"`
	} `json:"STATUS"`
}

// sendCommand sends a command to a CGMiner API endpoint
func (c *CGMinerClient) sendCommand(ctx context.Context, ip string, cmd CGMinerCommand) error {
	addr := fmt.Sprintf("%s:%d", ip, CGMinerAPIPort)

	// Create connection with timeout
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", addr, err)
	}
	defer conn.Close()

	// Set read/write deadlines
	deadline := time.Now().Add(5 * time.Second)
	_ = conn.SetDeadline(deadline)

	// CGMiner API uses a simple format: {"command":"cmd","parameter":"param"}
	cmdJSON, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	// Send command
	_, err = conn.Write(cmdJSON)
	if err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	// Read response (optional - we don't always need to wait for it)
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		// Timeout or connection closed - acceptable for fire-and-forget
		return nil
	}

	// Parse response to check for errors
	var resp CGMinerResponse
	// CGMiner sometimes returns null-terminated strings, strip them
	respStr := strings.TrimRight(string(buf[:n]), "\x00")
	if err := json.Unmarshal([]byte(respStr), &resp); err != nil {
		// Response parsing failed but command might have worked
		c.logger.Debugw("CGMiner response parse failed", "ip", ip, "response", respStr)
		return nil
	}

	// Check status
	if len(resp.Status) > 0 && resp.Status[0].Status == "E" {
		return fmt.Errorf("CGMiner error: %s", resp.Status[0].Msg)
	}

	return nil
}

// TriggerLEDCelebration sends LED command to turn on white LED on Avalon miner.
// This turns on the white "locate" LED as a visual indicator when a block is found.
// Avalon Nano 3S and other Avalon miners use: ascset|0,led,1-1 (LED on)
func (c *CGMinerClient) TriggerLEDCelebration(ctx context.Context, ip string) error {
	// Avalon LED command format: ascset|0,led,1-<value>
	// Values: 0=off, 1=on (white), 255=query
	cmd := CGMinerCommand{
		Command:   "ascset",
		Parameter: "0,led,1-1", // Turn white LED ON
	}

	err := c.sendCommand(ctx, ip, cmd)
	if err != nil {
		c.logger.Debugw("LED celebration command failed", "ip", ip, "error", err)
		return err
	}

	c.logger.Debugw("LED celebration triggered (white LED on)", "ip", ip)
	return nil
}

// ResetLED turns off the celebration LED on an Avalon miner.
func (c *CGMinerClient) ResetLED(ctx context.Context, ip string) error {
	cmd := CGMinerCommand{
		Command:   "ascset",
		Parameter: "0,led,1-0", // Turn LED OFF
	}

	err := c.sendCommand(ctx, ip, cmd)
	if err != nil {
		c.logger.Debugw("LED reset command failed", "ip", ip, "error", err)
		return err
	}

	c.logger.Debugw("LED reset (LED off)", "ip", ip)
	return nil
}

// CelebrationManager handles LED celebrations across all Avalon devices
type CelebrationManager struct {
	client   *CGMinerClient
	registry *DeviceHintsRegistry
	logger   *zap.SugaredLogger

	// Track celebration state
	celebrating   bool
	celebrateMu   sync.Mutex
	celebrateStop chan struct{}
}

// NewCelebrationManager creates a new celebration manager
func NewCelebrationManager(registry *DeviceHintsRegistry, logger *zap.SugaredLogger) *CelebrationManager {
	return &CelebrationManager{
		client:        NewCGMinerClient(logger),
		registry:      registry,
		logger:        logger,
		celebrateStop: make(chan struct{}),
	}
}

// TriggerBlockCelebration triggers LED celebration on all known Avalon devices.
// This is called when a block is found to provide visual feedback across the mining farm.
// The LEDs will automatically reset after the specified duration.
func (m *CelebrationManager) TriggerBlockCelebration(ctx context.Context, coinSymbol string, height uint64, durationHours int) {
	if m.registry == nil {
		m.logger.Warn("No device registry, skipping LED celebration")
		return
	}

	// Get all Avalon devices
	avalons := m.registry.GetAvalonDevices()
	allDevices := m.registry.GetAll()
	if len(avalons) == 0 {
		m.logger.Infow("No Avalon devices found in registry, skipping LED celebration",
			"totalDevices", len(allDevices),
			"hint", "Ensure Sentinel config miner names contain 'Avalon' or 'Canaan', or use the avalon/canaan config section",
		)
		return
	}

	// Cancel any previous celebration timer to prevent goroutine leak
	m.celebrateMu.Lock()
	if m.celebrating {
		close(m.celebrateStop)
		m.celebrateStop = make(chan struct{})
	}
	m.celebrating = true
	stopCh := m.celebrateStop
	m.celebrateMu.Unlock()

	m.logger.Infow("Triggering LED celebration on Avalon devices",
		"coin", coinSymbol,
		"height", height,
		"deviceCount", len(avalons),
		"durationHours", durationHours,
	)

	// Store device IPs for later reset
	deviceIPs := make([]string, len(avalons))
	for i, d := range avalons {
		deviceIPs[i] = d.IP
	}

	// Send LED ON command to all Avalon devices concurrently
	m.sendLEDCommand(ctx, avalons, true, "celebration")

	// Schedule LED reset after celebration duration
	if durationHours > 0 {
		go func() {
			resetDuration := time.Duration(durationHours) * time.Hour
			m.logger.Infow("LED reset scheduled",
				"coin", coinSymbol,
				"resetIn", resetDuration,
			)

			select {
			case <-time.After(resetDuration):
				m.logger.Infow("Resetting Avalon LEDs after celebration",
					"coin", coinSymbol,
					"deviceCount", len(deviceIPs),
				)
				resetCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				m.resetLEDs(resetCtx, deviceIPs)
			case <-ctx.Done():
				// Context cancelled, reset LEDs immediately
				m.logger.Debug("Celebration cancelled, resetting LEDs")
				resetCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				m.resetLEDs(resetCtx, deviceIPs)
			case <-stopCh:
				// New celebration started, this goroutine is superseded
				m.logger.Debug("Celebration superseded by new block celebration")
				return
			}

			m.celebrateMu.Lock()
			m.celebrating = false
			m.celebrateMu.Unlock()
		}()
	}
}

// sendLEDCommand sends LED on/off command to all devices
func (m *CelebrationManager) sendLEDCommand(ctx context.Context, devices []*DeviceHint, turnOn bool, action string) {
	var wg sync.WaitGroup
	for _, device := range devices {
		wg.Add(1)
		go func(ip string, model string) {
			defer wg.Done()

			var err error
			if turnOn {
				err = m.client.TriggerLEDCelebration(ctx, ip)
			} else {
				err = m.client.ResetLED(ctx, ip)
			}

			if err != nil {
				m.logger.Warnw("LED command failed",
					"action", action,
					"turnOn", turnOn,
					"ip", ip,
					"model", model,
					"error", err,
				)
			} else {
				m.logger.Debugw("LED command sent",
					"action", action,
					"turnOn", turnOn,
					"ip", ip,
					"model", model,
				)
			}
		}(device.IP, device.DeviceModel)
	}

	// Wait for all commands to complete (with timeout)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger.Debugw("LED commands complete", "action", action, "deviceCount", len(devices))
	case <-time.After(10 * time.Second):
		m.logger.Warn("LED commands timed out (some devices may not have responded)")
	case <-ctx.Done():
		m.logger.Debug("LED commands cancelled")
	}
}

// resetLEDs turns off LEDs on the given device IPs
func (m *CelebrationManager) resetLEDs(ctx context.Context, ips []string) {
	var wg sync.WaitGroup
	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			if err := m.client.ResetLED(ctx, ip); err != nil {
				m.logger.Debugw("LED reset failed", "ip", ip, "error", err)
			}
		}(ip)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger.Info("All Avalon LEDs reset")
	case <-time.After(10 * time.Second):
		m.logger.Warn("LED reset timed out")
	}
}
