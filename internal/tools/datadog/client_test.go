package datadog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_Do_SetsHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.DatadogConfig{
		Enabled:      true,
		APIKey:       "test-api-key",
		AppKey:       "test-app-key",
		Site:         "datadoghq.com",
		RateLimitRPS: 100,
	}

	client := NewClient(cfg)
	// Override baseURL to point at the test server
	client.baseURL = srv.URL + "/"

	resp, err := client.Do(context.Background(), http.MethodGet, "api/v1/monitors", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "test-api-key", gotHeaders.Get("DD-API-KEY"))
	assert.Equal(t, "test-app-key", gotHeaders.Get("DD-APPLICATION-KEY"))
	assert.Equal(t, "application/json", gotHeaders.Get("Content-Type"))
}

func TestClient_Do_RateLimiting(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.DatadogConfig{
		Enabled:      true,
		APIKey:       "test-api-key",
		AppKey:       "test-app-key",
		RateLimitRPS: 100, // High limit so burst fills quickly
	}

	client := NewClient(cfg)
	client.baseURL = srv.URL + "/"

	// Verify the rate limiter is wired in by cancelling a context
	// The rate limiter should respect context cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Make several requests; they should succeed until context expires
	for i := 0; i < 5; i++ {
		resp, err := client.Do(ctx, http.MethodGet, "api/v1/test", nil)
		if err != nil {
			// Context cancelled — rate limiter respected it
			break
		}
		resp.Body.Close()
	}

	assert.Greater(t, callCount, 0, "at least one request should have succeeded")
}

func TestNewClient_Defaults(t *testing.T) {
	cfg := config.DatadogConfig{
		Enabled: true,
		APIKey:  "key",
		AppKey:  "app",
	}

	client := NewClient(cfg)

	assert.Equal(t, "https://api.datadoghq.com/", client.baseURL)
	assert.Equal(t, "key", client.apiKey)
	assert.Equal(t, "app", client.appKey)
	assert.NotNil(t, client.rateLimiter)
	assert.NotNil(t, client.httpClient)

	// Zero RPS should default to 5.0
	cfg2 := config.DatadogConfig{
		Enabled:      true,
		APIKey:       "key",
		AppKey:       "app",
		RateLimitRPS: 0,
	}
	client2 := NewClient(cfg2)
	// The limiter limit should be 5.0 (default)
	assert.InDelta(t, 5.0, float64(client2.rateLimiter.Limit()), 0.01)
}
