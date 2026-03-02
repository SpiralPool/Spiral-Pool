// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// WebhookClient delivers Sentinel alert payloads to configured webhook endpoints.
// It supports auto-detection of Discord and Telegram endpoints for native formatting.
type WebhookClient struct {
	endpoints []config.WebhookConfig
	client    *http.Client
	logger    *zap.SugaredLogger
	hostname  string
}

// WebhookPayload is the canonical alert payload sent to generic endpoints.
type WebhookPayload struct {
	AlertType string      `json:"alert_type"`
	Severity  string      `json:"severity"` // critical, warning, info
	Coin      string      `json:"coin,omitempty"`
	Message   string      `json:"message"`
	Details   interface{} `json:"details,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
	PoolID    string      `json:"pool_id,omitempty"`
	Hostname  string      `json:"hostname"`
}

// Discord webhook structures

type discordWebhook struct {
	Username string         `json:"username"`
	Embeds   []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields,omitempty"`
	Timestamp   string         `json:"timestamp"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

// Telegram structures

type telegramMessage struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// NewWebhookClient creates a new webhook delivery client.
// M13: hostnameOverride replaces os.Hostname() when set (for NAT/container environments).
// M10: Logs a warning for any endpoint not using HTTPS.
func NewWebhookClient(endpoints []config.WebhookConfig, logger *zap.Logger, hostnameOverride string) *WebhookClient {
	hostname := hostnameOverride
	if hostname == "" {
		hostname, _ = os.Hostname()
	}

	sugar := logger.Sugar()

	// M10: Warn about non-HTTPS webhook endpoints at construction time
	for i, ep := range endpoints {
		if ep.URL != "" && !strings.HasPrefix(strings.ToLower(ep.URL), "https://") {
			sugar.Warnw("Webhook endpoint does not use HTTPS — alert payloads may be transmitted in plaintext",
				"index", i,
				"url", truncateURL(ep.URL),
			)
		}
	}

	return &WebhookClient{
		endpoints: endpoints,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger:   sugar,
		hostname: hostname,
	}
}

// Send delivers a payload to all configured webhook endpoints.
// Fire-and-forget: errors are logged but do not block the sentinel loop.
func (w *WebhookClient) Send(ctx context.Context, payload WebhookPayload) {
	if len(w.endpoints) == 0 {
		return
	}

	payload.Hostname = w.hostname
	payload.Timestamp = time.Now()

	for _, ep := range w.endpoints {
		go w.sendToEndpoint(ctx, ep, payload)
	}
}

func (w *WebhookClient) sendToEndpoint(ctx context.Context, ep config.WebhookConfig, payload WebhookPayload) {
	var body []byte
	var err error

	switch {
	case isDiscordWebhook(ep.URL):
		body, err = w.formatDiscord(payload)
	case isTelegramWebhook(ep.URL):
		body, err = w.formatTelegram(payload, ep.ChatID)
	default:
		body, err = json.Marshal(payload)
	}

	if err != nil {
		w.logger.Errorw("Failed to marshal webhook payload",
			"url", truncateURL(ep.URL),
			"error", err,
		)
		return
	}

	// Attempt delivery with retries on 5xx/429 using exponential backoff
	maxAttempts := 3
	backoff := 2 * time.Second
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			// Exponential backoff: 2s, 4s, 8s...
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}

		if sendErr := w.doPost(ctx, ep, body); sendErr != nil {
			w.logger.Warnw("Webhook delivery failed",
				"url", truncateURL(ep.URL),
				"attempt", attempt+1,
				"error", sendErr,
			)
			continue
		}
		return // Success
	}

	w.logger.Errorw("Webhook delivery failed after retries",
		"url", truncateURL(ep.URL),
		"alertType", payload.AlertType,
		"severity", payload.Severity,
	)
}

func (w *WebhookClient) doPost(ctx context.Context, ep config.WebhookConfig, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// SEC-05: Generic User-Agent without version number to avoid fingerprinting
	req.Header.Set("User-Agent", "SpiralPool-Webhook")

	for k, v := range ep.Headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP POST: %w", err)
	}
	defer resp.Body.Close()

	// M9: Handle HTTP 429 (Too Many Requests) with exponential backoff
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		w.logger.Warnw("Webhook rate limited (429), backing off",
			"url", truncateURL(ep.URL),
			"retryAfter", retryAfter,
		)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryAfter):
		}
		return fmt.Errorf("rate limited: HTTP 429 (retrying after %s)", retryAfter)
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("server error: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("client error: HTTP %d", resp.StatusCode)
	}

	return nil
}

// parseRetryAfter parses the Retry-After header value.
// Supports both delta-seconds ("60") and HTTP-date formats.
// Returns a default backoff of 5 seconds if the header is missing or unparseable.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 5 * time.Second
	}

	// Try parsing as delta-seconds first
	if seconds, err := strconv.Atoi(value); err == nil {
		d := time.Duration(seconds) * time.Second
		// Cap at 5 minutes to avoid indefinite waits
		if d > 5*time.Minute {
			d = 5 * time.Minute
		}
		return d
	}

	// Try parsing as HTTP-date (RFC1123)
	if t, err := time.Parse(time.RFC1123, value); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 1 * time.Second
		}
		if d > 5*time.Minute {
			d = 5 * time.Minute
		}
		return d
	}

	return 5 * time.Second
}

// formatDiscord creates a Discord embed webhook payload.
func (w *WebhookClient) formatDiscord(payload WebhookPayload) ([]byte, error) {
	color := severityToDiscordColor(payload.Severity)

	var fields []discordField
	if payload.Coin != "" {
		fields = append(fields, discordField{Name: "Coin", Value: payload.Coin, Inline: true})
	}
	if payload.PoolID != "" {
		fields = append(fields, discordField{Name: "Pool", Value: payload.PoolID, Inline: true})
	}
	fields = append(fields, discordField{Name: "Host", Value: payload.Hostname, Inline: true})

	if payload.Details != nil {
		detailJSON, _ := json.Marshal(payload.Details)
		if len(detailJSON) > 0 && string(detailJSON) != "null" {
			detailStr := string(detailJSON)
			if len(detailStr) > 1024 {
				detailStr = detailStr[:1021] + "..."
			}
			fields = append(fields, discordField{Name: "Details", Value: "```json\n" + detailStr + "\n```", Inline: false})
		}
	}

	dw := discordWebhook{
		Username: "Spiral Sentinel",
		Embeds: []discordEmbed{
			{
				Title:       fmt.Sprintf("[%s] %s", strings.ToUpper(payload.Severity), payload.AlertType),
				Description: payload.Message,
				Color:       color,
				Fields:      fields,
				Timestamp:   payload.Timestamp.Format(time.RFC3339),
			},
		},
	}

	return json.Marshal(dw)
}

// formatTelegram creates a Telegram Bot API sendMessage payload.
func (w *WebhookClient) formatTelegram(payload WebhookPayload, chatID string) ([]byte, error) {
	if chatID == "" {
		return nil, fmt.Errorf("telegram webhook requires chat_id")
	}

	icon := severityToIcon(payload.Severity)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s <b>[%s] %s</b>\n", icon, strings.ToUpper(payload.Severity), payload.AlertType))
	sb.WriteString(payload.Message + "\n")
	if payload.Coin != "" {
		sb.WriteString(fmt.Sprintf("<b>Coin:</b> %s\n", payload.Coin))
	}
	if payload.PoolID != "" {
		sb.WriteString(fmt.Sprintf("<b>Pool:</b> %s\n", payload.PoolID))
	}
	sb.WriteString(fmt.Sprintf("<b>Host:</b> %s\n", payload.Hostname))
	sb.WriteString(fmt.Sprintf("<i>%s</i>", payload.Timestamp.Format(time.RFC3339)))

	msg := telegramMessage{
		ChatID:    chatID,
		Text:      sb.String(),
		ParseMode: "HTML",
	}

	return json.Marshal(msg)
}

// isDiscordWebhook detects Discord webhook URLs.
func isDiscordWebhook(url string) bool {
	return strings.Contains(url, "discord.com/api/webhooks/") ||
		strings.Contains(url, "discordapp.com/api/webhooks/")
}

// isTelegramWebhook detects Telegram Bot API URLs.
func isTelegramWebhook(url string) bool {
	return strings.Contains(url, "api.telegram.org/bot")
}

// severityToDiscordColor maps severity to Discord embed color.
func severityToDiscordColor(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 0xFF0000 // Red
	case "warning":
		return 0xFF8C00 // Dark orange
	default:
		return 0x00FF00 // Green (info)
	}
}

// severityToIcon maps severity to a text icon for Telegram.
func severityToIcon(severity string) string {
	switch strings.ToLower(severity) {
	case "critical":
		return "[!!!]"
	case "warning":
		return "[!]"
	default:
		return "[i]"
	}
}

// truncateURL returns a safe-to-log version of a URL with all sensitive path segments redacted.
// M6: Redacts the full URL path (not just query params) to prevent Telegram bot token leakage
// via /bot<token>/ path segments and Discord webhook tokens in /webhooks/<id>/<token>.
func truncateURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		// Fallback for unparseable URLs: truncate
		if len(rawURL) > 30 {
			return rawURL[:27] + "..."
		}
		return rawURL
	}
	// Return only scheme + host, redacting all path/query/fragment
	return parsed.Scheme + "://" + parsed.Host + "/***"
}
