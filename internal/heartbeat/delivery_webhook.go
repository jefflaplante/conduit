package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"conduit/internal/config"
)

// WebhookDeliverer delivers alerts to generic HTTP webhook endpoints.
type WebhookDeliverer struct{ client *http.Client }

// NewWebhookDeliverer creates a new webhook deliverer.
func NewWebhookDeliverer() *WebhookDeliverer { return &WebhookDeliverer{client: &http.Client{}} }

// Type returns the deliverer type identifier.
func (w *WebhookDeliverer) Type() string { return "webhook" }

// webhookPayload is the JSON structure sent to webhook endpoints.
type webhookPayload struct {
	ID        string                 `json:"id"`
	Severity  string                 `json:"severity"`
	Title     string                 `json:"title"`
	Message   string                 `json:"message"`
	Details   string                 `json:"details,omitempty"`
	Source    string                 `json:"source"`
	Component string                 `json:"component"`
	Timestamp string                 `json:"timestamp"`
	Tags      []string               `json:"tags,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// Deliver sends an alert to the configured webhook URL.
func (w *WebhookDeliverer) Deliver(ctx context.Context, alert Alert, target config.AlertTarget) error {
	url, ok := target.Config["url"]
	if !ok || url == "" {
		return fmt.Errorf("webhook target %q missing required 'url' config", target.Name)
	}

	timeout := 30 * time.Second
	if timeoutStr, ok := target.Config["timeout_seconds"]; ok {
		if secs, err := strconv.Atoi(timeoutStr); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}

	payload := webhookPayload{
		ID:        alert.ID,
		Severity:  string(alert.Severity),
		Title:     alert.Title,
		Message:   alert.Message,
		Details:   alert.Details,
		Source:    alert.Source,
		Component: alert.Component,
		Timestamp: alert.CreatedAt.Format(time.RFC3339),
		Tags:      alert.Tags,
		Metadata:  alert.Metadata,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("webhook returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
