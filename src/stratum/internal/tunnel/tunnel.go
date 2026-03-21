// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package tunnel provides interfaces and types for external access tunneling.
// This package supports multiple tunnel backends (Cloudflare Tunnel, etc.)
// for exposing the stratum pool to external miners.
package tunnel

import (
	"context"
	"time"
)

// Tunnel defines the interface for tunnel implementations.
// Implementations manage the lifecycle of a tunnel process that
// exposes the local stratum port to external connections.
type Tunnel interface {
	// Start begins the tunnel process. Returns when tunnel is running.
	// Context cancellation will stop the tunnel.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the tunnel process.
	Stop(ctx context.Context) error

	// IsRunning returns true if the tunnel process is currently active.
	IsRunning() bool

	// Restart stops and starts the tunnel process.
	Restart(ctx context.Context) error

	// Status returns the current tunnel status.
	Status() Status

	// Endpoint returns the public endpoint for the tunnel.
	Endpoint() string
}

// Status represents the current state of a tunnel.
type Status struct {
	// Running indicates if the tunnel process is currently active.
	Running bool `json:"running"`

	// PID is the process ID of the tunnel process (0 if not running).
	PID int `json:"pid,omitempty"`

	// StartTime is when the tunnel was last started.
	StartTime time.Time `json:"startTime,omitempty"`

	// Uptime is how long the tunnel has been running.
	Uptime time.Duration `json:"uptime,omitempty"`

	// LastError is the most recent error encountered.
	LastError string `json:"lastError,omitempty"`

	// LastErrorTime is when the last error occurred.
	LastErrorTime time.Time `json:"lastErrorTime,omitempty"`

	// Restarts is the number of times the tunnel has been restarted.
	Restarts int `json:"restarts"`

	// ConnectedAt is when the tunnel successfully connected.
	ConnectedAt time.Time `json:"connectedAt,omitempty"`

	// Endpoint is the public endpoint URL.
	Endpoint string `json:"endpoint,omitempty"`

	// Mode is the tunnel type (e.g., "cloudflare", "port-forward").
	Mode string `json:"mode"`
}

// Mode represents the type of external access mode.
type Mode string

const (
	// ModePortForward uses traditional port forwarding through router/firewall.
	ModePortForward Mode = "port-forward"

	// ModeTunnel uses a tunnel service (e.g., Cloudflare Tunnel) for NAT traversal.
	ModeTunnel Mode = "tunnel"
)

// PortForwardConfig holds configuration for port-forward mode.
type PortForwardConfig struct {
	// PublicHost is the public hostname or IP address.
	PublicHost string `yaml:"publicHost"`

	// PublicPort is the externally accessible port.
	PublicPort int `yaml:"publicPort"`

	// LocalPort is the internal stratum port to forward to.
	LocalPort int `yaml:"localPort"`
}

// TunnelConfig holds configuration for tunnel mode.
type TunnelConfig struct {
	// Name is the tunnel identifier.
	Name string `yaml:"name"`

	// ConfigPath is the path to the tunnel config file.
	ConfigPath string `yaml:"configPath"`

	// CredentialsPath is the path to the credentials file.
	CredentialsPath string `yaml:"credentialsPath"`

	// Hostname is the public hostname for the tunnel.
	Hostname string `yaml:"hostname"`

	// BinaryPath is the path to the tunnel binary (e.g., cloudflared).
	BinaryPath string `yaml:"binaryPath"`
}

// SecurityConfig holds security settings for external access.
type SecurityConfig struct {
	// HardenOnEnable automatically applies stricter limits when external access is enabled.
	HardenOnEnable bool `yaml:"hardenOnEnable"`

	// RequireTLS forces TLS for external connections.
	RequireTLS bool `yaml:"requireTLS"`

	// MaxConnectionsPerIP overrides the normal limit when external access is enabled.
	MaxConnectionsPerIP int `yaml:"maxConnectionsPerIP"`

	// SharesPerSecond overrides the normal rate limit when external access is enabled.
	SharesPerSecond int `yaml:"sharesPerSecond"`

	// OriginalMaxConnPerIP stores the original value before hardening.
	OriginalMaxConnPerIP int `yaml:"originalMaxConnPerIP,omitempty"`

	// OriginalSharesPerSec stores the original value before hardening.
	OriginalSharesPerSec int `yaml:"originalSharesPerSec,omitempty"`

	// OriginalBanThreshold stores the original value before hardening.
	OriginalBanThreshold int `yaml:"originalBanThreshold,omitempty"`

	// OriginalBanDuration stores the original value before hardening.
	OriginalBanDuration string `yaml:"originalBanDuration,omitempty"`
}

// ExternalConfig holds the complete external access configuration.
type ExternalConfig struct {
	// Enabled indicates if external access is currently active.
	Enabled bool `yaml:"enabled"`

	// Mode is the access mode (port-forward or tunnel).
	Mode Mode `yaml:"mode"`

	// PortForward holds port-forward specific settings.
	PortForward PortForwardConfig `yaml:"portForward"`

	// Tunnel holds tunnel-specific settings.
	Tunnel TunnelConfig `yaml:"tunnel"`

	// Security holds security hardening settings.
	Security SecurityConfig `yaml:"security"`
}

// DefaultExternalConfig returns an ExternalConfig with sensible defaults.
func DefaultExternalConfig() ExternalConfig {
	return ExternalConfig{
		Enabled: false,
		Mode:    ModeTunnel,
		PortForward: PortForwardConfig{
			PublicPort: 3333,
			LocalPort:  3333,
		},
		Tunnel: TunnelConfig{
			BinaryPath: "/usr/local/bin/cloudflared",
		},
		Security: SecurityConfig{
			HardenOnEnable:      true,
			RequireTLS:          false,
			MaxConnectionsPerIP: 50,
			SharesPerSecond:     500,
		},
	}
}

// Validate checks the ExternalConfig for required fields based on mode.
func (c *ExternalConfig) Validate() error {
	if !c.Enabled {
		return nil // No validation needed if disabled
	}

	switch c.Mode {
	case ModePortForward:
		if c.PortForward.PublicHost == "" {
			return &ValidationError{Field: "portForward.publicHost", Message: "required when mode is port-forward"}
		}
		if c.PortForward.PublicPort <= 0 || c.PortForward.PublicPort > 65535 {
			return &ValidationError{Field: "portForward.publicPort", Message: "must be between 1 and 65535"}
		}
		if c.PortForward.LocalPort <= 0 || c.PortForward.LocalPort > 65535 {
			return &ValidationError{Field: "portForward.localPort", Message: "must be between 1 and 65535"}
		}
	case ModeTunnel:
		if c.Tunnel.Name == "" {
			return &ValidationError{Field: "tunnel.name", Message: "required when mode is tunnel"}
		}
		if c.Tunnel.Hostname == "" {
			return &ValidationError{Field: "tunnel.hostname", Message: "required when mode is tunnel"}
		}
	default:
		return &ValidationError{Field: "mode", Message: "must be 'port-forward' or 'tunnel'"}
	}

	return nil
}

// GetPublicEndpoint returns the public endpoint URL based on the mode.
func (c *ExternalConfig) GetPublicEndpoint() string {
	switch c.Mode {
	case ModePortForward:
		if c.PortForward.PublicPort == 3333 {
			return "stratum+tcp://" + c.PortForward.PublicHost + ":3333"
		}
		return "stratum+tcp://" + c.PortForward.PublicHost + ":" + itoa(c.PortForward.PublicPort)
	case ModeTunnel:
		// Cloudflare tunnels typically use port 443
		return "stratum+tcp://" + c.Tunnel.Hostname + ":443"
	default:
		return ""
	}
}

// itoa is a simple int to string conversion.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// ValidationError represents a configuration validation error.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "external." + e.Field + ": " + e.Message
}
