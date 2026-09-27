package ai

import (
	"context"
	"fmt"
	"testing"

	"conduit/internal/config"
)

// conduit-31jg.68(2): only the FIRST call of a turn had quota fallback
// (callWithRecovery). A quota error in a tool-loop round killed the turn.
// contextGuardProvider — which every later call goes through — now falls
// back the same way, sticks to the fallback route for the rest of the turn,
// and meters every attempt exactly once.

func newToolLoopFallbackRouter(t *testing.T, rounds int) (*Router, *MockProvider, *MockProvider, *countingObserver) {
	t.Helper()
	r, err := NewRouterWithExecution(config.AIConfig{}, nil, &sumToolLoopEngine{rounds: rounds})
	if err != nil {
		t.Fatal(err)
	}
	primary := NewMockProvider("primary")
	fallback := NewMockProvider("fallbackprov")
	r.RegisterProvider("primary", primary)
	r.RegisterProvider("fallbackprov", fallback)
	r.mu.Lock()
	r.providerMeta["primary"] = ProviderMeta{Name: "primary", Type: "anthropic", DefaultModel: "claude-haiku-4-5-20251001", FallbackModel: "fallbackprov/fb-model-x"}
	r.providerMeta["fallbackprov"] = ProviderMeta{Name: "fallbackprov", Type: "openai", DefaultModel: "fb-model-x"}
	r.mu.Unlock()
	obs := &countingObserver{}
	r.GetUsageTracker().SetObserver(obs)
	return r, primary, fallback, obs
}

var errToolLoopQuota = fmt.Errorf(`API error: 400 - {"type":"error","error":{"type":"invalid_request_error","message":"You're out of extra usage."}}`)

func TestToolLoopQuotaError_FallsBackAndSticks(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := map[bool]string{false: "non-streaming", true: "streaming"}[streaming]
		t.Run(name, func(t *testing.T) {
			r, primary, fallback, obs := newToolLoopFallbackRouter(t, 2)
			tc := []ToolCall{{ID: "t1", Name: "Bash", Args: map[string]interface{}{}}}
			primary.SetResponses([]MockResponse{
				{ToolCalls: tc, Usage: Usage{PromptTokens: 10}}, // first call (callWithRecovery)
				{Error: errToolLoopQuota},                       // tool round 1 → quota
				{Content: "WRONG: primary used again after quota"},
			})
			fallback.SetResponses([]MockResponse{
				{ToolCalls: tc, Usage: Usage{PromptTokens: 20}}, // round 1 on fallback
				{Content: "fallback final", Usage: Usage{PromptTokens: 30}},
			})

			var resp ConversationResponse
			var err error
			if streaming {
				resp, err = r.GenerateResponseStreaming(context.Background(), newFallbackSession(t), "go", "primary", "claude-haiku-4-5-20251001", func(string, bool) {})
			} else {
				resp, err = r.GenerateResponseWithToolsAndProgress(context.Background(), newFallbackSession(t), "go", "primary", "claude-haiku-4-5-20251001", nil)
			}
			if err != nil {
				t.Fatalf("tool-loop quota error killed the turn: %v", err)
			}
			if resp.GetContent() != "fallback final" {
				t.Errorf("content = %q, want %q", resp.GetContent(), "fallback final")
			}
			if n := primary.GetCallCount(); n != 2 {
				t.Errorf("primary calls = %d, want 2 (first call + the round that hit quota; later rounds stick to fallback)", n)
			}
			if n := fallback.GetCallCount(); n != 2 {
				t.Errorf("fallback calls = %d, want 2", n)
			}
			for i, c := range fallback.GetCalls() {
				if c.Request.Model != "fallbackprov/fb-model-x" {
					t.Errorf("fallback call %d model = %q", i, c.Request.Model)
				}
			}
			assertNoFallbackModelOnPrimary(t, primary, "fallbackprov/fb-model-x")

			obs.mu.Lock()
			defer obs.mu.Unlock()
			if obs.errors != 1 {
				t.Errorf("metered errors = %d, want 1 (the quota attempt)", obs.errors)
			}
			if got := obs.models["primary|claude-haiku-4-5-20251001"]; got != 1 {
				t.Errorf("metered primary calls = %d (all %v), want 1", got, obs.models)
			}
			if got := obs.models["fallbackprov|fallbackprov/fb-model-x"]; got != 2 {
				t.Errorf("metered fallback calls = %d (all %v), want 2", got, obs.models)
			}
		})
	}
}

func TestToolLoopQuotaError_NoFallbackRouteSurfaces(t *testing.T) {
	r, primary, fallback, _ := newToolLoopFallbackRouter(t, 1)
	r.mu.Lock()
	meta := r.providerMeta["primary"]
	meta.FallbackModel = "nowhere/unknown"
	r.providerMeta["primary"] = meta
	r.mu.Unlock()
	tc := []ToolCall{{ID: "t1", Name: "Bash", Args: map[string]interface{}{}}}
	primary.SetResponses([]MockResponse{{ToolCalls: tc}, {Error: errToolLoopQuota}})

	_, err := r.GenerateResponseWithToolsAndProgress(context.Background(), newFallbackSession(t), "go", "primary", "claude-haiku-4-5-20251001", nil)
	if err == nil {
		t.Fatal("expected the quota error to surface without a fallback route")
	}
	if fallback.GetCallCount() != 0 {
		t.Errorf("fallback calls = %d, want 0", fallback.GetCallCount())
	}
}

// The EmptyGuard failover provider is itself guarded; it must not fall back
// again (no third route).
func TestEmptyFailoverGuard_DoesNotChainQuotaFallback(t *testing.T) {
	r, _, fallback, _ := newToolLoopFallbackRouter(t, 0)
	third := NewMockProvider("third")
	r.RegisterProvider("third", third)
	r.mu.Lock()
	r.providerMeta["third"] = ProviderMeta{Name: "third", Type: "openai", DefaultModel: "t"}
	fb := r.providerMeta["fallbackprov"]
	fb.FallbackModel = "third/t"
	r.providerMeta["fallbackprov"] = fb
	r.mu.Unlock()
	fallback.SetResponses([]MockResponse{{Error: errToolLoopQuota}})

	_, p, ok := r.ResolveEmptyFailover("primary")
	if !ok {
		t.Fatal("expected a failover route")
	}
	if _, err := p.GenerateResponse(context.Background(), &GenerateRequest{Model: "fallbackprov/fb-model-x"}); err == nil {
		t.Fatal("expected the quota error to surface from the failover guard")
	}
	if third.GetCallCount() != 0 {
		t.Errorf("failover guard chained to a third route (%d calls)", third.GetCallCount())
	}
}
