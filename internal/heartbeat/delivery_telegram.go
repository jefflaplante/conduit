package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"conduit/internal/config"
)

// TelegramDeliverer implements Deliverer for sending alerts via Telegram Bot API.
type TelegramDeliverer struct {
	client *http.Client
}

// NewTelegramDeliverer creates a new Telegram deliverer with a 30-second timeout.
func NewTelegramDeliverer() *TelegramDeliverer {
	return &TelegramDeliverer{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Type returns the deliverer type identifier.
func (d *TelegramDeliverer) Type() string {
	return "telegram"
}

// telegramRequest represents the JSON body for sendMessage API.
type telegramRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// telegramResponse represents the Telegram API response.
type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
}

// Deliver sends an alert to a Telegram chat.
// Requires "bot_token" and "chat_id" in target.Config.
func (d *TelegramDeliverer) Deliver(ctx context.Context, alert Alert, target config.AlertTarget) error {
	// Extract required configuration
	botToken, ok := target.Config["bot_token"]
	if !ok || botToken == "" {
		return fmt.Errorf("missing required config: bot_token")
	}

	chatID, ok := target.Config["chat_id"]
	if !ok || chatID == "" {
		return fmt.Errorf("missing required config: chat_id")
	}

	// Format the alert message using existing function from processor.go
	message := formatAlertForTelegram(alert)

	// Build request body
	reqBody := telegramRequest{
		ChatID:    chatID,
		Text:      message,
		ParseMode: "Markdown",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build API URL
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Send request
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Handle rate limiting (429)
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("telegram rate limit exceeded (429): please retry later")
	}

	// Parse Telegram response
	var telegramResp telegramResponse
	if err := json.Unmarshal(respBody, &telegramResp); err != nil {
		return fmt.Errorf("failed to parse telegram response: %w", err)
	}

	// Check for Telegram API errors
	if !telegramResp.OK {
		return fmt.Errorf("telegram API error (code %d): %s", telegramResp.ErrorCode, telegramResp.Description)
	}

	return nil
}
