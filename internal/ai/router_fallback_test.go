package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// bd-27ud: quota fallback must route to the fallback model's own provider,
// not reuse the failed provider. Previously: haiku quota error on anthropic →
// fallback "z-ai/glm-5.3" sent to anthropic → 404 not_found (journal 15:33:21Z
// Sep 1 2026, 5 "silent" sub-agent deaths).

// newFallbackTestRouter builds a router with two mock providers:
// "primary" (anthropic-like) and "fallbackprov" (z-ai-like), where
// primary's configured fallback model points at fallbackprov.
func newFallbackTestRouter(t *testing.T) (*Router, *MockProvider, *MockProvider) {
	t.Helper()

	cfg := config.AIConfig{} // no providers from config; we register mocks
	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	primary := NewMockProvider("primary")
	fallback := NewMockProvider("fallbackprov")
	router.RegisterProvider("primary", primary)
	router.RegisterProvider("fallbackprov", fallback)

	// providerMeta entries so GetProviderMeta + ResolveProviderForModel work
	router.mu.Lock()
	router.providerMeta["primary"] = ProviderMeta{
		Name:          "primary",
		Type:          "anthropic",
		DefaultModel:  "claude-haiku-4-5-20251001",
		FallbackModel: "fallbackprov/fb-model-x",
	}
	router.providerMeta["fallbackprov"] = ProviderMeta{
		Name:         "fallbackprov",
		Type:         "openai",
		DefaultModel: "fb-model-x",
	}
	router.mu.Unlock()

	return router, primary, fallback
}

func TestQuotaFallbackRoutesToOwnProvider_GenerateResponse(t *testing.T) {
	router, primary, fallback := newFallbackTestRouter(t)

	// Primary fails with a quota error; fallback provider succeeds.
	primary.SetResponses([]MockResponse{
		{Error: fmt.Errorf("400 - out of extra usage: quota exhausted")},
	})
	fallback.AddResponse("fallback ok", nil)

	resp, err := router.GenerateResponse(context.Background(), newFallbackSession(t), "hello", "primary")
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if resp.Content != "fallback ok" {
		t.Fatalf("expected fallback content, got %q", resp.Content)
	}

	// The retry must have gone through the fallback provider.
	if fallback.GetCallCount() != 1 {
		t.Errorf("expected 1 call on fallback provider, got %d", fallback.GetCallCount())
	}
	// And it must NOT have been retried on the primary provider.
	if primary.GetCallCount() != 1 {
		t.Errorf("expected primary to be called exactly once (no wrong-provider retry), got %d", primary.GetCallCount())
	}
	// The fallback request must carry the fallback model (prefix stripped by
	// the provider itself in the real OpenAI provider; mock just records it).
	if calls := fallback.GetCalls(); len(calls) == 1 && calls[0].Request.Model != "fallbackprov/fb-model-x" {
		t.Errorf("expected fallback model %q, got %q", "fallbackprov/fb-model-x", calls[0].Request.Model)
	}
}

func TestQuotaFallbackRoutesToOwnProvider_ToolLoop(t *testing.T) {
	router, primary, fallback := newFallbackTestRouter(t)

	primary.SetResponses([]MockResponse{
		{Error: fmt.Errorf("400 quota exceeded for model claude-haiku-4-5")},
	})
	fallback.AddResponse("tool loop fallback ok", nil)

	// GenerateResponseWithTools is the path sub-agents actually hit
	// (SubAgent Error line in the journal).
	resp, err := router.GenerateResponseWithToolsAndProgress(context.Background(), newFallbackSession(t), "do the thing", "primary", "claude-haiku-4-5-20251001", nil)
	if err != nil {
		t.Fatalf("GenerateResponseWithToolsAndProgress: %v", err)
	}
	if resp == nil || resp.GetContent() != "tool loop fallback ok" {
		t.Fatalf("expected fallback content, got %+v", resp)
	}
	if fallback.GetCallCount() != 1 {
		t.Errorf("expected 1 fallback call, got %d", fallback.GetCallCount())
	}
}

func TestQuotaFallbackRoutesToOwnProvider_Streaming(t *testing.T) {
	router, primary, fallback := newFallbackTestRouter(t)

	primary.SetResponses([]MockResponse{
		{Error: fmt.Errorf("400 - usage limit exceeded: out of extra usage")},
	})
	fallback.AddResponse("stream fallback ok", nil)

	resp, err := router.GenerateResponseStreaming(context.Background(), newFallbackSession(t), "hello stream", "primary", "claude-haiku-4-5-20251001", func(delta string, done bool) {})
	if err != nil {
		t.Fatalf("GenerateResponseStreaming: %v", err)
	}
	if resp == nil || resp.GetContent() != "stream fallback ok" {
		t.Fatalf("expected fallback content, got %+v", resp)
	}
	if fallback.GetCallCount() != 1 {
		t.Errorf("expected 1 fallback call, got %d", fallback.GetCallCount())
	}
}

func TestQuotaFallback_NoFallbackWhenProviderUnresolvable(t *testing.T) {
	// A fallback model with no resolvable provider must NOT be retried on the
	// failed provider (the original bug). It should surface the original error.
	router, primary, fallback := newFallbackTestRouter(t)
	_ = fallback

	// Point primary's fallback at a model whose provider doesn't exist.
	router.mu.Lock()
	meta := router.providerMeta["primary"]
	meta.FallbackModel = "nosuchprov/fb-model-x"
	router.providerMeta["primary"] = meta
	router.mu.Unlock()

	primary.SetResponses([]MockResponse{
		{Error: fmt.Errorf("400 - out of extra usage")},
	})

	resp, err := router.GenerateResponse(context.Background(), newFallbackSession(t), "hello", "primary")
	if err == nil {
		t.Fatalf("expected original quota error to surface, got resp %+v", resp)
	}
	if !strings.Contains(err.Error(), "out of extra usage") {
		t.Errorf("expected original quota error, got %v", err)
	}
	// No wrong-provider retry.
	if primary.GetCallCount() != 1 {
		t.Errorf("expected primary called once (no blind retry), got %d", primary.GetCallCount())
	}
}

func TestQuotaFallback_DefaultFallbackModelResolves(t *testing.T) {
	// When FallbackModel is unset, the default "z-ai/glm-5.3" is used. With a
	// z-ai provider registered, the retry must go there — not to the primary.
	router, primary, _ := newFallbackTestRouter(t)

	router.mu.Lock()
	meta := router.providerMeta["primary"]
	meta.FallbackModel = "" // force default
	router.providerMeta["primary"] = meta
	router.providerMeta["z-ai"] = ProviderMeta{
		Name:         "z-ai",
		Type:         "openai",
		DefaultModel: "glm-5.3",
	}
	router.mu.Unlock()

	zai := NewMockProvider("z-ai")
	router.RegisterProvider("z-ai", zai)

	primary.SetResponses([]MockResponse{
		{Error: fmt.Errorf("400 - out of extra usage")},
	})
	zai.AddResponse("zai ok", nil)

	resp, err := router.GenerateResponse(context.Background(), newFallbackSession(t), "hello", "primary")
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if resp.Content != "zai ok" {
		t.Fatalf("expected zai content, got %q", resp.Content)
	}
	if zai.GetCallCount() != 1 {
		t.Errorf("expected 1 zai call, got %d", zai.GetCallCount())
	}
	if primary.GetCallCount() != 1 {
		t.Errorf("expected primary called once, got %d", primary.GetCallCount())
	}
}

// newTestSession creates a minimal session for router tests.
func newFallbackSession(t *testing.T) *sessions.Session {
	t.Helper()
	return &sessions.Session{Key: "test-session-" + t.Name()}
}
