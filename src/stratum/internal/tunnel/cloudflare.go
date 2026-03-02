// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package tunnel provides Cloudflare Tunnel management for external access.
package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CloudflareTunnel manages a cloudflared tunnel process for exposing
// the stratum pool to external miners without router configuration.
type CloudflareTunnel struct {
	cfg        CloudflareConfig
	cmd        *exec.Cmd
	mu         sync.RWMutex
	wg         sync.WaitGroup
	running    bool
	startTime  time.Time
	restarts   int
	lastError  string
	lastErrAt  time.Time
	cancelFunc context.CancelFunc
	done       chan struct{}
}

// CloudflareConfig holds configuration for the Cloudflare Tunnel.
type CloudflareConfig struct {
	// BinaryPath is the path to the cloudflared binary.
	// Default: /usr/local/bin/cloudflared
	BinaryPath string

	// ConfigPath is the path to the cloudflared config.yml file.
	// If empty, a config will be generated.
	ConfigPath string

	// CredentialsPath is the path to the tunnel credentials JSON file.
	// This file is created when running `cloudflared tunnel create`.
	CredentialsPath string

	// TunnelName is the name of the tunnel to run.
	TunnelName string

	// Hostname is the public hostname for the tunnel.
	Hostname string

	// StratumPort is the local stratum port to expose.
	StratumPort int

	// LogLevel sets the cloudflared log level (debug, info, warn, error).
	LogLevel string

	// MetricsAddr is the address for cloudflared metrics endpoint.
	// Default: 127.0.0.1:2000
	MetricsAddr string
}

// NewCloudflareTunnel creates a new CloudflareTunnel with the given configuration.
func NewCloudflareTunnel(cfg CloudflareConfig) (*CloudflareTunnel, error) {
	// Validate required fields
	if cfg.BinaryPath == "" {
		cfg.BinaryPath = "/usr/local/bin/cloudflared"
	}
	if cfg.TunnelName == "" {
		return nil, fmt.Errorf("tunnel name is required")
	}
	if cfg.Hostname == "" {
		return nil, fmt.Errorf("hostname is required")
	}
	if cfg.StratumPort <= 0 || cfg.StratumPort > 65535 {
		return nil, fmt.Errorf("stratum port must be between 1 and 65535")
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.MetricsAddr == "" {
		cfg.MetricsAddr = "127.0.0.1:2000"
	}

	return &CloudflareTunnel{
		cfg:  cfg,
		done: make(chan struct{}),
	}, nil
}

// Start begins the cloudflared tunnel process.
func (ct *CloudflareTunnel) Start(ctx context.Context) error {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	if ct.running {
		return fmt.Errorf("tunnel is already running")
	}

	// Verify cloudflared binary exists
	if _, err := os.Stat(ct.cfg.BinaryPath); os.IsNotExist(err) {
		return fmt.Errorf("cloudflared binary not found at %s", ct.cfg.BinaryPath)
	}

	// Verify credentials file exists
	if ct.cfg.CredentialsPath != "" {
		if _, err := os.Stat(ct.cfg.CredentialsPath); os.IsNotExist(err) {
			return fmt.Errorf("credentials file not found at %s", ct.cfg.CredentialsPath)
		}
	}

	// Generate config if needed
	if ct.cfg.ConfigPath == "" {
		configPath, err := ct.generateConfig()
		if err != nil {
			return fmt.Errorf("failed to generate config: %w", err)
		}
		ct.cfg.ConfigPath = configPath
	}

	// Build command arguments
	args := []string{
		"tunnel",
		"--config", ct.cfg.ConfigPath,
		"--loglevel", ct.cfg.LogLevel,
		"--metrics", ct.cfg.MetricsAddr,
		"run",
		ct.cfg.TunnelName,
	}

	// Create cancellable context
	tunnelCtx, cancel := context.WithCancel(ctx)
	ct.cancelFunc = cancel

	// Create the command
	ct.cmd = exec.CommandContext(tunnelCtx, ct.cfg.BinaryPath, args...)

	// Capture stdout/stderr for monitoring
	stdout, err := ct.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := ct.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the process
	if err := ct.cmd.Start(); err != nil {
		ct.lastError = err.Error()
		ct.lastErrAt = time.Now()
		return fmt.Errorf("failed to start cloudflared: %w", err)
	}

	ct.running = true
	ct.startTime = time.Now()
	ct.done = make(chan struct{})

	// Monitor output in background
	ct.wg.Add(2)
	go func() {
		defer ct.wg.Done()
		ct.monitorOutput(stdout, "stdout")
	}()
	go func() {
		defer ct.wg.Done()
		ct.monitorOutput(stderr, "stderr")
	}()

	// Wait for process in background
	ct.wg.Add(1)
	go func() {
		defer ct.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "PANIC recovered in process wait goroutine: %v\n", r)
			}
		}()
		err := ct.cmd.Wait()
		ct.mu.Lock()
		ct.running = false
		if err != nil {
			ct.lastError = err.Error()
			ct.lastErrAt = time.Now()
		}
		close(ct.done)
		ct.mu.Unlock()
	}()

	return nil
}

// Stop gracefully shuts down the tunnel process.
func (ct *CloudflareTunnel) Stop(ctx context.Context) error {
	ct.mu.Lock()

	if !ct.running {
		ct.mu.Unlock()
		return nil
	}

	// Cancel the context to signal shutdown
	if ct.cancelFunc != nil {
		ct.cancelFunc()
	}

	// Get the done channel while holding the lock
	done := ct.done
	ct.mu.Unlock()

	// Wait for process to exit or context to timeout
	select {
	case <-done:
		// Process exited; wait for all monitor goroutines to finish
		ct.wg.Wait()
		return nil
	case <-ctx.Done():
		// Force kill if graceful shutdown timed out
		ct.mu.Lock()
		if ct.cmd != nil && ct.cmd.Process != nil {
			_ = ct.cmd.Process.Kill()
		}
		ct.running = false
		ct.mu.Unlock()
		// Wait for all monitor goroutines to finish after kill
		ct.wg.Wait()
		return ctx.Err()
	}
}

// IsRunning returns true if the tunnel process is currently active.
func (ct *CloudflareTunnel) IsRunning() bool {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.running
}

// Restart stops and starts the tunnel process.
func (ct *CloudflareTunnel) Restart(ctx context.Context) error {
	// Stop with a timeout
	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := ct.Stop(stopCtx); err != nil {
		// Log but continue with restart
		ct.mu.Lock()
		ct.lastError = "stop during restart: " + err.Error()
		ct.lastErrAt = time.Now()
		ct.mu.Unlock()
	}

	// Wait a moment for cleanup
	time.Sleep(500 * time.Millisecond)

	// Start
	if err := ct.Start(ctx); err != nil {
		return err
	}

	ct.mu.Lock()
	ct.restarts++
	ct.mu.Unlock()

	return nil
}

// Status returns the current tunnel status.
func (ct *CloudflareTunnel) Status() Status {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	status := Status{
		Running:   ct.running,
		Restarts:  ct.restarts,
		LastError: ct.lastError,
		Mode:      "cloudflare",
		Endpoint:  ct.Endpoint(),
	}

	if ct.running {
		status.StartTime = ct.startTime
		status.Uptime = time.Since(ct.startTime)
		if ct.cmd != nil && ct.cmd.Process != nil {
			status.PID = ct.cmd.Process.Pid
		}
	}

	if !ct.lastErrAt.IsZero() {
		status.LastErrorTime = ct.lastErrAt
	}

	return status
}

// Endpoint returns the public endpoint for the tunnel.
func (ct *CloudflareTunnel) Endpoint() string {
	return "stratum+tcp://" + ct.cfg.Hostname + ":443"
}

// generateConfig creates a cloudflared config.yml file.
func (ct *CloudflareTunnel) generateConfig() (string, error) {
	// Create config directory
	configDir := filepath.Dir(ct.cfg.ConfigPath)
	if configDir == "" || configDir == "." {
		configDir = "/etc/spiralpool/cloudflared"
	}
	if err := os.MkdirAll(configDir, 0750); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := filepath.Join(configDir, "config.yml")

	// Build config content
	var sb strings.Builder
	sb.WriteString("# Cloudflared tunnel configuration for Spiral Pool\n")
	sb.WriteString("# Generated by spiralctl external setup\n\n")
	sb.WriteString("tunnel: ")
	sb.WriteString(ct.cfg.TunnelName)
	sb.WriteString("\n")

	if ct.cfg.CredentialsPath != "" {
		sb.WriteString("credentials-file: ")
		sb.WriteString(ct.cfg.CredentialsPath)
		sb.WriteString("\n")
	}

	sb.WriteString("\ningress:\n")
	sb.WriteString("  # Stratum protocol routing\n")
	sb.WriteString("  - hostname: ")
	sb.WriteString(ct.cfg.Hostname)
	sb.WriteString("\n")
	sb.WriteString("    service: tcp://localhost:")
	sb.WriteString(fmt.Sprintf("%d", ct.cfg.StratumPort))
	sb.WriteString("\n")
	sb.WriteString("  # Catch-all for unmatched requests\n")
	sb.WriteString("  - service: http_status:404\n")

	// Write config file
	if err := os.WriteFile(configPath, []byte(sb.String()), 0600); err != nil {
		return "", fmt.Errorf("failed to write config: %w", err)
	}

	return configPath, nil
}

// monitorOutput reads from a pipe and logs the output.
func (ct *CloudflareTunnel) monitorOutput(pipe interface{ Read([]byte) (int, error) }, name string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "PANIC recovered in monitorOutput(%s): %v\n", name, r)
		}
	}()
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		line := scanner.Text()
		// Check for errors in output
		if strings.Contains(strings.ToLower(line), "error") ||
			strings.Contains(strings.ToLower(line), "failed") {
			ct.mu.Lock()
			ct.lastError = line
			ct.lastErrAt = time.Now()
			ct.mu.Unlock()
		}
		// In production, this would log to the pool's logger
		// For now, we just consume the output
	}
}

// VerifyInstallation checks if cloudflared is properly installed.
func VerifyInstallation(binaryPath string) error {
	if binaryPath == "" {
		binaryPath = "/usr/local/bin/cloudflared"
	}

	// Check if binary exists
	info, err := os.Stat(binaryPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("cloudflared not found at %s", binaryPath)
	}
	if err != nil {
		return fmt.Errorf("failed to stat cloudflared: %w", err)
	}

	// Check if it's executable
	if info.Mode()&0111 == 0 {
		return fmt.Errorf("cloudflared is not executable")
	}

	// Try to get version
	cmd := exec.Command(binaryPath, "version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to run cloudflared version: %w", err)
	}

	// Parse version from output
	version := strings.TrimSpace(string(output))
	if version == "" {
		return fmt.Errorf("cloudflared returned empty version")
	}

	return nil
}

// ListTunnels returns a list of configured tunnels.
func ListTunnels(binaryPath string) ([]string, error) {
	if binaryPath == "" {
		binaryPath = "/usr/local/bin/cloudflared"
	}

	cmd := exec.Command(binaryPath, "tunnel", "list")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list tunnels: %w", err)
	}

	var tunnels []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		// Skip header line
		if strings.HasPrefix(line, "ID") || strings.TrimSpace(line) == "" {
			continue
		}
		// Parse tunnel name (second column)
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			tunnels = append(tunnels, fields[1])
		}
	}

	return tunnels, nil
}

// TunnelExists checks if a tunnel with the given name exists.
func TunnelExists(binaryPath, tunnelName string) (bool, error) {
	tunnels, err := ListTunnels(binaryPath)
	if err != nil {
		return false, err
	}

	for _, t := range tunnels {
		if t == tunnelName {
			return true, nil
		}
	}

	return false, nil
}

// FindCredentialsFile looks for the credentials file for a tunnel.
func FindCredentialsFile(tunnelName string) (string, error) {
	// Common locations for credentials files
	paths := []string{
		filepath.Join("/etc/spiralpool/cloudflared", tunnelName+".json"),
		filepath.Join("/root/.cloudflared", tunnelName+".json"),
		filepath.Join(os.Getenv("HOME"), ".cloudflared", tunnelName+".json"),
	}

	// Also check for any .json file in the default directories
	dirs := []string{
		"/etc/spiralpool/cloudflared",
		"/root/.cloudflared",
		filepath.Join(os.Getenv("HOME"), ".cloudflared"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	// Search directories for any credentials file
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				return filepath.Join(dir, entry.Name()), nil
			}
		}
	}

	return "", fmt.Errorf("credentials file not found for tunnel '%s'", tunnelName)
}
