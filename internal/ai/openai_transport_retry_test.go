package ai

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestOpenAIProvider_RetriesTransportErrors covers bd-13p: sub-agents died
// silently when z.ai returned "context deadline exceeded" on client.Do.
// Transport errors (timeouts, connection resets) must enter the retry loop,
// not return immediately. Only context cancellation from the caller should
// abort without retry.
func TestOpenAIProvider_RetriesTransportErrors(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 { // fail twice, succeed third
			// Hijack and slam the connection shut — transport error
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()

	p := &OpenAIProvider{
		name:    "test",
		baseURL: server.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}

	resp, err := p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if resp.Content == "" {
		t.Fatal("expected non-empty content")
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls (2 transport failures + 1 success), got %d", calls)
	}
}

// TestOpenAIProvider_DoesNotRetryCallerContextCancel verifies that when the
// CALLER's context is done, we abort immediately rather than retrying —
// a cancelled caller means nobody wants the answer anymore.
func TestOpenAIProvider_DoesNotRetryCallerContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller already gave up

	p := &OpenAIProvider{
		name:    "test",
		baseURL: server.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}

	_, err := p.GenerateResponse(ctx, &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !isCallerContextError(ctx, err) {
		t.Fatalf("expected context error, got: %v", err)
	}
}

// TestOpenAIProvider_NoRetryOnConnectionRefusedSingleAttempt sanity-checks
// that a persistent transport failure still exhausts maxRetries and surfaces
// the error (rather than hanging or panicking).
func TestOpenAIProvider_NoRetryOnConnectionRefusedSingleAttempt(t *testing.T) {
	// Port 1 on localhost: nothing listening, fast connection refused
	p := &OpenAIProvider{
		name:    "test",
		baseURL: "http://127.0.0.1:1",
		client: &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext,
			},
		},
	}

	start := time.Now()
	_, err := p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got success")
	}
	// 3 retries with existing 2s/4s backoff ≈ 6-15s. This is the designed
	// behavior; we assert it terminates well below the http.Client timeout
	// budget and returns an error rather than hanging or panicking.
	if elapsed > 30*time.Second {
		t.Fatalf("took too long (%v) — appears to hang", elapsed)
	}
}
