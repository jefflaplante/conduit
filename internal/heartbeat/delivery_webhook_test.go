package heartbeat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookDeliverer_Type(t *testing.T) {
	d := NewWebhookDeliverer()
	assert.Equal(t, "webhook", d.Type())
}

func TestWebhookDeliverer_Deliver_Success(t *testing.T) {
	var received webhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &received))

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	d := NewWebhookDeliverer()
	alert := Alert{
		ID:        "test-123",
		Severity:  AlertSeverityWarning,
		Title:     "Test Alert",
		Message:   "This is a test message",
		Details:   "Additional details here",
		Source:    "test-source",
		Component: "test-component",
		CreatedAt: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		Tags:      []string{"test", "webhook"},
		Metadata:  map[string]interface{}{"key": "value"},
	}
	target := config.AlertTarget{
		Name:   "test-webhook",
		Type:   "webhook",
		Config: map[string]string{"url": server.URL},
	}

	err := d.Deliver(context.Background(), alert, target)
	require.NoError(t, err)

	assert.Equal(t, "test-123", received.ID)
	assert.Equal(t, "warning", received.Severity)
	assert.Equal(t, "Test Alert", received.Title)
	assert.Equal(t, "This is a test message", received.Message)
	assert.Equal(t, "Additional details here", received.Details)
	assert.Equal(t, "test-source", received.Source)
	assert.Equal(t, "test-component", received.Component)
	assert.Equal(t, "2025-01-15T10:30:00Z", received.Timestamp)
	assert.Equal(t, []string{"test", "webhook"}, received.Tags)
	assert.Equal(t, "value", received.Metadata["key"])
}

func TestWebhookDeliverer_Deliver_MissingURL(t *testing.T) {
	d := NewWebhookDeliverer()
	alert := Alert{ID: "test-123"}
	target := config.AlertTarget{
		Name:   "bad-target",
		Type:   "webhook",
		Config: map[string]string{},
	}

	err := d.Deliver(context.Background(), alert, target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required 'url' config")
}

func TestWebhookDeliverer_Deliver_Non2xxResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	d := NewWebhookDeliverer()
	alert := Alert{ID: "test-123", CreatedAt: time.Now()}
	target := config.AlertTarget{
		Name:   "failing-webhook",
		Type:   "webhook",
		Config: map[string]string{"url": server.URL},
	}

	err := d.Deliver(context.Background(), alert, target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
	assert.Contains(t, err.Error(), "internal error")
}

func TestWebhookDeliverer_Deliver_CustomTimeout(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	d := NewWebhookDeliverer()
	alert := Alert{ID: "test-123", CreatedAt: time.Now()}
	target := config.AlertTarget{
		Name: "timeout-webhook",
		Type: "webhook",
		Config: map[string]string{
			"url":             server.URL,
			"timeout_seconds": "5",
		},
	}

	err := d.Deliver(context.Background(), alert, target)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestWebhookDeliverer_Deliver_ContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	d := NewWebhookDeliverer()
	alert := Alert{ID: "test-123", CreatedAt: time.Now()}
	target := config.AlertTarget{
		Name:   "cancel-webhook",
		Type:   "webhook",
		Config: map[string]string{"url": server.URL},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := d.Deliver(ctx, alert, target)
	require.Error(t, err)
}
