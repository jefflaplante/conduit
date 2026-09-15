package ai

import (
	"context"
	"testing"

	"conduit/internal/sessions"
)

// conduit-14qr: GenerateResponseStreaming historically bypassed the empty
// guard — raw-empty provider responses (z.ai HTTP-200 empty payloads, 2026-09-14
// RCA) flowed straight to the gateway delivery path and died as silent WARN
// suppressions. These tests prove the streaming path now runs the same
// retry → cross-model failover → visible fallback machinery as the
// non-streaming chain (conduit-18vj/1z0g), and that deliberate silence and
// non-empty responses pass through untouched.
//
// Setup mirrors newFailoverTestRouter: real Router, mock providers, z-ai's
// fallback_model pointing at a claude-* model that Tier-1 heuristic routes to
// the anthropic provider.

func newStreamingGuardRouter(t *testing.T) (*Router, *MockProvider, *MockProvider) {
	t.Helper()
	r := newFailoverTestRouter()
	r.providerMeta["z-ai"] = ProviderMeta{Name: "z-ai", Type: "openai", FallbackModel: "claude-sonnet-4-6"}
	t.Cleanup(func() { SetEmptyFailoverRouter(nil) })
	SetEmptyFailoverRouter(r)
	return r, r.providers["z-ai"].(*MockProvider), r.providers["anthropic"].(*MockProvider)
}

func TestStreamingEmptyGuard_RetryRecovers(t *testing.T) {
	r, zai, anthropic := newStreamingGuardRouter(t)
	zai.SetResponses([]MockResponse{
		{Content: ""}, // original call: raw empty (the failure under test)
		{Content: "recovered via retry"},
	})

	resp, err := r.GenerateResponseStreaming(context.Background(), &sessions.Session{}, "test message", "z-ai", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetContent() != "recovered via retry" {
		t.Fatalf("expected retry content, got %q", resp.GetContent())
	}
	if len(zai.calls) != 2 {
		t.Fatalf("expected original + 1 retry on z-ai, got %d calls", len(zai.calls))
	}
	if len(anthropic.calls) != 0 {
		t.Fatalf("retry succeeded — failover must not fire, got %d anthropic calls", len(anthropic.calls))
	}
}

func TestStreamingEmptyGuard_FailoverRecovers(t *testing.T) {
	r, zai, anthropic := newStreamingGuardRouter(t)
	zai.SetResponses([]MockResponse{
		{Content: ""}, // original
		{Content: ""}, // same-model retry also empty
	})
	anthropic.SetResponses([]MockResponse{
		{Content: "failover rescue"},
	})

	resp, err := r.GenerateResponseStreaming(context.Background(), &sessions.Session{}, "test message", "z-ai", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetContent() != "failover rescue" {
		t.Fatalf("expected failover content, got %q", resp.GetContent())
	}
	if len(zai.calls) != 2 {
		t.Fatalf("expected original + 1 retry on z-ai, got %d calls", len(zai.calls))
	}
	if len(anthropic.calls) != 1 {
		t.Fatalf("expected exactly 1 failover call on anthropic, got %d", len(anthropic.calls))
	}
}

func TestStreamingEmptyGuard_AllEmptyDeliversFallbackText(t *testing.T) {
	r, zai, anthropic := newStreamingGuardRouter(t)
	zai.SetResponses([]MockResponse{
		{Content: ""}, // original
		{Content: ""}, // retry
	})
	anthropic.SetResponses([]MockResponse{
		{Content: ""}, // failover also dead
	})

	resp, err := r.GenerateResponseStreaming(context.Background(), &sessions.Session{}, "test message", "z-ai", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !IsEmptyResponseFallback(resp.GetContent()) {
		t.Fatalf("expected terminal fallback text, got %q", resp.GetContent())
	}
	if len(zai.calls) != 2 || len(anthropic.calls) != 1 {
		t.Fatalf("expected 2 z-ai calls + 1 anthropic call, got %d/%d", len(zai.calls), len(anthropic.calls))
	}
}

func TestStreamingEmptyGuard_NonEmptyAndSilencePassThrough(t *testing.T) {
	r, zai, anthropic := newStreamingGuardRouter(t)

	// Non-empty content: single call, untouched.
	zai.SetResponses([]MockResponse{{Content: "real answer"}})
	resp, err := r.GenerateResponseStreaming(context.Background(), &sessions.Session{}, "test message", "z-ai", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetContent() != "real answer" || len(zai.calls) != 1 {
		t.Fatalf("non-empty passthrough broken: content=%q calls=%d", resp.GetContent(), len(zai.calls))
	}

	// Deliberate silence (NO_REPLY) arrives NON-empty: the guard must not
	// touch it. Blanking happens later in silent-pattern processing; retrying
	// deliberate silence would double heartbeat loads and spam NO_REPLY turns.
	zai.SetResponses([]MockResponse{{Content: "NO_REPLY"}})
	zai.respIndex = 0
	zai.calls = nil
	resp, err = r.GenerateResponseStreaming(context.Background(), &sessions.Session{}, "test message", "z-ai", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetContent() != "NO_REPLY" {
		t.Fatalf("deliberate silence must pass through unguarded, got %q", resp.GetContent())
	}
	if len(zai.calls) != 1 || len(anthropic.calls) != 0 {
		t.Fatalf("silence must not trigger retry/failover, got zai=%d anthropic=%d calls", len(zai.calls), len(anthropic.calls))
	}
}
