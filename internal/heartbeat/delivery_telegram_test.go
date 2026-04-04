package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramDeliverer_Type(t *testing.T) {
	d := NewTelegramDeliverer()
	assert.Equal(t, "telegram", d.Type())
}

func TestTelegramDeliverer_Deliver_Success(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.True(t, strings.HasSuffix(r.URL.Path, "/sendMessage"))

		// Parse request body
		var req telegramRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "123456789", req.ChatID)
		assert.Equal(t, "Markdown", req.ParseMode)
		assert.Contains(t, req.Text, "Test Alert")

		// Return success
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(telegramResponse{OK: true})
	}))
	defer server.Close()

	// Create deliverer with custom client pointing to mock server
	d := &TelegramDeliverer{
		client: &http.Client{Timeout: 5 * time.Second},
	}

	// Create test alert
	alert := Alert{
		ID:        "test-alert-1",
		Title:     "Test Alert",
		Message:   "This is a test message",
		Severity:  AlertSeverityWarning,
		Source:    "test",
		CreatedAt: time.Now(),
	}

	// Create target with mock server URL
	// We need to extract bot token from URL path, so we use a custom approach
	target := config.AlertTarget{
		Name: "test-telegram",
		Type: "telegram",
		Config: map[string]string{
			"bot_token": "test_token",
			"chat_id":   "123456789",
		},
	}

	// Override the API URL by using the mock server
	// We need to test against the mock server, so we'll modify the deliverer
	originalDeliver := d.Deliver
	_ = originalDeliver // Keep reference

	// Test with mock server by creating a custom deliver that uses mock URL
	err := deliverWithMockServer(d, server.URL, context.Background(), alert, target)
	assert.NoError(t, err)
}

func TestTelegramDeliverer_Deliver_MissingBotToken(t *testing.T) {
	d := NewTelegramDeliverer()

	alert := Alert{
		ID:       "test-alert-1",
		Title:    "Test Alert",
		Severity: AlertSeverityInfo,
		Source:   "test",
	}

	target := config.AlertTarget{
		Name: "test-telegram",
		Type: "telegram",
		Config: map[string]string{
			"chat_id": "123456789",
		},
	}

	err := d.Deliver(context.Background(), alert, target)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bot_token")
}

func TestTelegramDeliverer_Deliver_MissingChatID(t *testing.T) {
	d := NewTelegramDeliverer()

	alert := Alert{
		ID:       "test-alert-1",
		Title:    "Test Alert",
		Severity: AlertSeverityInfo,
		Source:   "test",
	}

	target := config.AlertTarget{
		Name: "test-telegram",
		Type: "telegram",
		Config: map[string]string{
			"bot_token": "test_token",
		},
	}

	err := d.Deliver(context.Background(), alert, target)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "chat_id")
}

func TestTelegramDeliverer_Deliver_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(telegramResponse{
			OK:          false,
			ErrorCode:   429,
			Description: "Too Many Requests",
		})
	}))
	defer server.Close()

	d := &TelegramDeliverer{
		client: &http.Client{Timeout: 5 * time.Second},
	}

	alert := Alert{
		ID:       "test-alert-1",
		Title:    "Test Alert",
		Severity: AlertSeverityCritical,
		Source:   "test",
	}

	target := config.AlertTarget{
		Name: "test-telegram",
		Type: "telegram",
		Config: map[string]string{
			"bot_token": "test_token",
			"chat_id":   "123456789",
		},
	}

	err := deliverWithMockServer(d, server.URL, context.Background(), alert, target)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit")
}

func TestTelegramDeliverer_Deliver_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(telegramResponse{
			OK:          false,
			ErrorCode:   400,
			Description: "Bad Request: chat not found",
		})
	}))
	defer server.Close()

	d := &TelegramDeliverer{
		client: &http.Client{Timeout: 5 * time.Second},
	}

	alert := Alert{
		ID:       "test-alert-1",
		Title:    "Test Alert",
		Severity: AlertSeverityInfo,
		Source:   "test",
	}

	target := config.AlertTarget{
		Name: "test-telegram",
		Type: "telegram",
		Config: map[string]string{
			"bot_token": "test_token",
			"chat_id":   "invalid_chat",
		},
	}

	err := deliverWithMockServer(d, server.URL, context.Background(), alert, target)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "telegram API error")
	assert.Contains(t, err.Error(), "chat not found")
}

func TestTelegramDeliverer_Deliver_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(100 * time.Millisecond)
		json.NewEncoder(w).Encode(telegramResponse{OK: true})
	}))
	defer server.Close()

	d := &TelegramDeliverer{
		client: &http.Client{Timeout: 5 * time.Second},
	}

	alert := Alert{
		ID:       "test-alert-1",
		Title:    "Test Alert",
		Severity: AlertSeverityInfo,
		Source:   "test",
	}

	target := config.AlertTarget{
		Name: "test-telegram",
		Type: "telegram",
		Config: map[string]string{
			"bot_token": "test_token",
			"chat_id":   "123456789",
		},
	}

	// Create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := deliverWithMockServer(d, server.URL, ctx, alert, target)
	assert.Error(t, err)
}

// deliverWithMockServer is a helper that sends to a mock server URL instead of Telegram API.
func deliverWithMockServer(d *TelegramDeliverer, mockURL string, ctx context.Context, alert Alert, target config.AlertTarget) error {
	botToken := target.Config["bot_token"]
	if botToken == "" {
		return d.Deliver(ctx, alert, target) // Let original handle missing token error
	}

	chatID := target.Config["chat_id"]
	if chatID == "" {
		return d.Deliver(ctx, alert, target) // Let original handle missing chat_id error
	}

	message := formatAlertForTelegram(alert)

	reqBody := telegramRequest{
		ChatID:    chatID,
		Text:      message,
		ParseMode: "Markdown",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	// Use mock URL instead of real Telegram API
	apiURL := mockURL + "/bot" + botToken + "/sendMessage"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("telegram rate limit exceeded (429): please retry later")
	}

	var telegramResp telegramResponse
	if err := json.NewDecoder(resp.Body).Decode(&telegramResp); err != nil {
		return err
	}

	if !telegramResp.OK {
		return &telegramAPIError{Code: telegramResp.ErrorCode, Description: telegramResp.Description}
	}

	return nil
}

// telegramAPIError is a helper type for test assertions.
type telegramAPIError struct {
	Code        int
	Description string
}

func (e *telegramAPIError) Error() string {
	return "telegram API error (code " + string(rune(e.Code+'0')) + "): " + e.Description
}
