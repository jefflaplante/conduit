//go:build with_pagerduty

package pagerduty

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"conduit/internal/config"
)

func TestClient_Do_SetsHeaders(t *testing.T) {
	var gotAuth, gotContentType, gotAccept string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "my-secret-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	}

	client := NewClient(cfg)
	resp, err := client.Do(context.Background(), http.MethodGet, "/services", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Token token=my-secret-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Token token=my-secret-token")
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", gotContentType, "application/json")
	}
	if gotAccept != "application/vnd.pagerduty+json;version=2" {
		t.Errorf("Accept = %q, want %q", gotAccept, "application/vnd.pagerduty+json;version=2")
	}
}

func TestNewClient_Defaults(t *testing.T) {
	cfg := config.PagerDutyConfig{
		Enabled:  true,
		APIToken: "tok",
	}
	client := NewClient(cfg)

	if client.baseURL != "https://api.pagerduty.com" {
		t.Errorf("baseURL = %q, want default", client.baseURL)
	}
	if client.apiToken != "tok" {
		t.Errorf("apiToken = %q, want %q", client.apiToken, "tok")
	}
	if client.httpClient.Timeout.Seconds() != 30 {
		t.Errorf("timeout = %v, want 30s", client.httpClient.Timeout)
	}
	if client.rateLimiter == nil {
		t.Error("rateLimiter is nil")
	}
}
