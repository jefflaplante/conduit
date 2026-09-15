package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"conduit/internal/config"
)

// conduit-15gt: glm-5.3-flash reasoning exhaustion empties. Three fixes:
//
// 1. Thinking control — OpenAI-compatible providers can inject a `thinking`
//    object ({"type": "disabled"} or {"type": "enabled", "budget_tokens": N})
//    from provider config. Probed live 2026-09-15: z.ai honors disabled/enabled
//    but IGNORES budget_tokens as a cap (budget 50 → 125 reasoning tokens).
//
// 2. Budget accounting — when thinking is enabled with a budget, the visible-
//    output max_tokens is inflated by the budget so reasoning cannot starve
//    the answer (the "account for thinking when budgeting tokens" fix).
//
// 3. Empty-failover semantics — same-provider refusal is refined to
//    same-provider-AND-same-model. Failing over z-ai/glm-5.3-flash →
//    z-ai/glm-5.3 (different model on the same backend, different inference
//    path) is meaningful failover; retrying the identical model+backend pair
//    that just returned empty twice is not.

// newThinkingTestServer returns an httptest server that captures the request
// body and replies with a minimal valid completion.
func newThinkingTestServer(t *testing.T) (*httptest.Server, *[]byte) {
	t.Helper()
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"message": map[string]interface{}{
						"content": "pong",
					},
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
			},
		})
	}))
	t.Cleanup(server.Close)
	return server, &received
}

// capturedRequest decodes the captured request body into a map.
func capturedRequest(t *testing.T, raw *[]byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(*raw, &m); err != nil {
		t.Fatalf("failed to decode captured request: %v", err)
	}
	return m
}

// --- Thinking control (fix 1) ---

func TestOpenAIProvider_ThinkingDisabled_Injected(t *testing.T) {
	server, raw := newThinkingTestServer(t)
	p, err := NewOpenAIProvider(config.ProviderConfig{
		Name:    "z-ai",
		BaseURL: server.URL,
		Model:   "glm-5.3-flash",
		Thinking: &config.ThinkingConfig{
			Type: "disabled",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "reply pong"}},
		MaxTokens: 4000,
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	m := capturedRequest(t, raw)
	thinking, ok := m["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected thinking object in request, got: %v", m["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Fatalf("expected thinking.type=disabled, got %v", thinking["type"])
	}
	if _, hasBudget := thinking["budget_tokens"]; hasBudget {
		t.Fatalf("budget_tokens must be omitted when type=disabled")
	}
}

func TestOpenAIProvider_ThinkingEnabledWithBudget_Injected(t *testing.T) {
	server, raw := newThinkingTestServer(t)
	p, err := NewOpenAIProvider(config.ProviderConfig{
		Name:    "z-ai",
		BaseURL: server.URL,
		Model:   "glm-5.3-flash",
		Thinking: &config.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 2048,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "reply pong"}},
		MaxTokens: 4000,
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	m := capturedRequest(t, raw)
	thinking, ok := m["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected thinking object in request, got: %v", m["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("expected thinking.type=enabled, got %v", thinking["type"])
	}
	if thinking["budget_tokens"] != float64(2048) {
		t.Fatalf("expected budget_tokens=2048, got %v", thinking["budget_tokens"])
	}
}

func TestOpenAIProvider_NoThinkingConfig_OmitsParam(t *testing.T) {
	server, raw := newThinkingTestServer(t)
	p, err := NewOpenAIProvider(config.ProviderConfig{
		Name:    "local",
		BaseURL: server.URL,
		Model:   "llama3",
		// no Thinking — param must not be sent (ghost/local servers may not accept it)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	m := capturedRequest(t, raw)
	if _, has := m["thinking"]; has {
		t.Fatalf("thinking param must be omitted when not configured, got: %v", m["thinking"])
	}
}

func TestOpenAIProvider_Thinking_StreamingPath_Too(t *testing.T) {
	server, raw := newThinkingTestServer(t)
	p, err := NewOpenAIProvider(config.ProviderConfig{
		Name:    "z-ai",
		BaseURL: server.URL,
		Model:   "glm-5.3-flash",
		Thinking: &config.ThinkingConfig{
			Type: "disabled",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = p.GenerateResponseStreaming(context.Background(), &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "reply pong"}},
		MaxTokens: 4000,
	}, func(delta string, done bool) {})
	if err != nil {
		t.Fatalf("streaming request failed: %v", err)
	}

	m := capturedRequest(t, raw)
	thinking, ok := m["thinking"].(map[string]interface{})
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("streaming path must also inject thinking control, got: %v", m["thinking"])
	}
}

// --- Budget accounting (fix 2): max_tokens inflated by thinking budget ---

func TestOpenAIProvider_ThinkingBudget_InflatesMaxTokens(t *testing.T) {
	server, raw := newThinkingTestServer(t)
	p, err := NewOpenAIProvider(config.ProviderConfig{
		Name:    "z-ai",
		BaseURL: server.URL,
		Model:   "glm-5.3-flash",
		Thinking: &config.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: 1500,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "reply pong"}},
		MaxTokens: 4000,
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	m := capturedRequest(t, raw)
	if m["max_tokens"] != float64(5500) {
		t.Fatalf("expected max_tokens 4000+1500=5500 on the wire, got %v", m["max_tokens"])
	}
}

func TestOpenAIProvider_ThinkingDisabled_MaxTokensUntouched(t *testing.T) {
	server, raw := newThinkingTestServer(t)
	p, err := NewOpenAIProvider(config.ProviderConfig{
		Name:    "z-ai",
		BaseURL: server.URL,
		Model:   "glm-5.3-flash",
		Thinking: &config.ThinkingConfig{
			Type:         "disabled",
			BudgetTokens: 1500, // set but meaningless when disabled — must be ignored
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "reply pong"}},
		MaxTokens: 4000,
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	m := capturedRequest(t, raw)
	if m["max_tokens"] != float64(4000) {
		t.Fatalf("disabled thinking must not touch max_tokens, got %v", m["max_tokens"])
	}
}

// --- Empty-failover semantics (fix 3) — refusal lives at guard level (req.Model known only there) ---

func TestEmptyFailoverAttempt_SameProviderSameModelViaResolverRefused(t *testing.T) {
	r := newFailoverTestRouter()
	// Misconfiguration shape preserved from bd-27ud: z-ai's fallback_model
	// names the SAME model the provider serves by default. The pure
	// resolver returns the route; the guard refuses it (fail-closed on
	// same-model, prefix-stripped compare, empty = unverifiable).
	r.providerMeta["z-ai"] = ProviderMeta{Name: "z-ai", Type: "openai", FallbackModel: "z-ai/glm-5.3-flash"}
	SetEmptyFailoverRouter(r)
	defer SetEmptyFailoverRouter(nil)

	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{{Content: "   "}})
	pathological := NewMockProvider("z-ai") // must NEVER be called
	pathological.SetResponses([]MockResponse{{Content: "should never be reached"}})
	_ = pathological

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "z-ai/glm-5.3-flash"}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("guard must not error on refused failover: %v", err)
	}
	if !strings.Contains(resp.Content, "empty response") {
		t.Fatalf("expected visible fallback after same-model refusal, got %q", resp.Content)
	}
}

func TestEmptyFailoverAttempt_SameProviderDifferentModel_Executes(t *testing.T) {
	// Guard-level: resolver returns the same provider (z-ai) but a different
	// model. The attempt must EXECUTE (not refuse) and strip the provider
	// prefix before sending (bd-27ud wire rule).
	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{
		{Content: "   "}, // same-model retry ALSO empty
	})
	failover := NewMockProvider("z-ai")
	failover.SetResponses([]MockResponse{
		{Content: "recovered on glm-5.3"},
	})
	stub := &failoverRouterStub{model: "z-ai/glm-5.3", provider: failover, ok: true}
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "z-ai/glm-5.3-flash"}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "recovered on glm-5.3" {
		t.Fatalf("expected failover content, got %q", resp.Content)
	}
	if failover.GetCallCount() != 1 {
		t.Fatalf("expected exactly 1 failover call, got %d", failover.GetCallCount())
	}
}

func TestEmptyFailoverAttempt_SameProviderSameModel_Refused(t *testing.T) {
	// Resolver pathologically returns the same provider AND the same model
	// the request already uses — must refuse and deliver visible fallback.
	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{
		{Content: "   "}, // same-model retry ALSO empty
	})
	pathological := NewMockProvider("z-ai")
	pathological.SetResponses([]MockResponse{
		{Content: "should never be reached"},
	})
	stub := &failoverRouterStub{model: "z-ai/glm-5.3-flash", provider: pathological, ok: true}
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "z-ai/glm-5.3-flash"}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("guard must not error on refused failover: %v", err)
	}
	if resp.Content == "should never be reached" {
		t.Fatal("same-model failover must never execute")
	}
	if pathological.GetCallCount() != 0 {
		t.Fatalf("same-model failover must never call the backend, got %d calls", pathological.GetCallCount())
	}
}
