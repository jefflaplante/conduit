package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"conduit/internal/config"
)

// TestIsTransientTimeoutError tests the timeout classifier used by the
// retry-once logic (bd-13p).
func TestIsTransientTimeoutError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "raw context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "wrapped deadline exceeded (errors.Is path)",
			err:  fmt.Errorf("provider call: %w", context.DeadlineExceeded),
			want: true,
		},
		{
			name: "http client timeout string form",
			err:  errors.New(`Get "https://api.example.com/v1/chat": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`),
			want: true,
		},
		{
			name: "context canceled — NOT retryable (intentional cancellation)",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "quota 400 — not a timeout",
			err:  errors.New("API error: 400 - {\"error\":{\"message\":\"out of extra usage\"}}"),
			want: false,
		},
		{
			name: "generic server error",
			err:  errors.New("API error: 500 - internal server error"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTransientTimeoutError(tt.err)
			if got != tt.want {
				t.Errorf("IsTransientTimeoutError() = %v, want %v", tt.want, got)
			}
		})
	}
}

// TestGenerateResponse_RetriesOnceOnTimeout verifies that a transient
// deadline-exceeded error triggers exactly one retry (bd-13p).
func TestGenerateResponse_RetriesOnceOnTimeout(t *testing.T) {
	router, err := NewRouter(configWithMockProvider(), nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	mock := NewMockProvider("mock")
	mock.SetResponses([]MockResponse{
		{Error: fmt.Errorf("post https://api.z.ai/api/paas/v4/chat/completions: context deadline exceeded")},
		{Content: "recovered"},
	})
	router.RegisterProvider("mock", mock)

	session := newTestSession()
	resp, err := router.GenerateResponse(context.Background(), session, "hi", "mock")
	if err != nil {
		t.Fatalf("Expected retry to succeed, got error: %v", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("Expected recovered response, got %q", resp.Content)
	}
	if calls := mock.GetCallCount(); calls != 2 {
		t.Errorf("Expected exactly 2 calls (1 original + 1 retry), got %d", calls)
	}
}

// TestGenerateResponse_NoRetryWhenParentContextDead verifies that when the
// caller's context has already expired, no retry is attempted (bd-13p) —
// retrying against a dead context would fail instantly.
func TestGenerateResponse_NoRetryWhenParentContextDead(t *testing.T) {
	router, err := NewRouter(configWithMockProvider(), nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	mock := NewMockProvider("mock")
	mock.AddErrorResponse(fmt.Errorf("provider call: %w", context.DeadlineExceeded))
	router.RegisterProvider("mock", mock)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	time.Sleep(60 * time.Millisecond) // let the parent deadline lapse

	session := newTestSession()
	_, err = router.GenerateResponse(ctx, session, "hi", "mock")
	if err == nil {
		t.Fatal("Expected error to propagate when parent context is dead")
	}
	if calls := mock.GetCallCount(); calls != 1 {
		t.Errorf("Expected exactly 1 call (no retry on dead parent), got %d", calls)
	}
}

// TestGenerateResponse_NoRetryOnContextCanceled verifies intentional
// cancellation propagates without retry (bd-13p).
func TestGenerateResponse_NoRetryOnContextCanceled(t *testing.T) {
	router, err := NewRouter(configWithMockProvider(), nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	mock := NewMockProvider("mock")
	mock.AddErrorResponse(context.Canceled)
	router.RegisterProvider("mock", mock)

	session := newTestSession()
	_, err = router.GenerateResponse(context.Background(), session, "hi", "mock")
	if err == nil {
		t.Fatal("Expected canceled error to propagate")
	}
	if calls := mock.GetCallCount(); calls != 1 {
		t.Errorf("Expected exactly 1 call (no retry on cancel), got %d", calls)
	}
}

// configWithMockProvider returns a minimal AIConfig pointing at the mock provider.
func configWithMockProvider() config.AIConfig {
	return config.AIConfig{
		DefaultProvider: "mock",
		Providers:       []config.ProviderConfig{},
	}
}
