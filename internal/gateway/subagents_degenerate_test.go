package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
)

// bd-1k3o: a sub-agent whose final answer is the empty-guard fallback text
// (locally generated, NOT a model answer — see internal/ai/empty_guard.go)
// must be routed to the failure path: error message in the session,
// WakeSourceSubAgentFailed to the parent. Not "Completed".

// subAgentFallbackRouter wires a Router whose default provider returns the
// configured mock responses, so the sub-agent goroutine runs a full
// (mock) LLM chain.
func subAgentFallbackRouter(t *testing.T, responses []ai.MockResponse) *ai.Router {
	t.Helper()
	router, err := ai.NewRouter(config.AIConfig{DefaultProvider: "mock"}, nil)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	provider := ai.NewMockProvider("mock")
	provider.SetResponses(responses)
	router.RegisterProvider("mock", provider)
	return router
}

func TestSubAgent_EmptyGuardFallback_RoutedToFailure(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.ctx = context.Background()
	gw.ai = subAgentFallbackRouter(t, []ai.MockResponse{
		// Model returns raw-empty twice (original + guard retry) → guard
		// substitutes fallback text → sub-agent must treat as FAILURE.
		{Content: ""},
		{Content: ""},
	})

	sessionKey, err := gw.SpawnSubAgent(context.Background(), "do the thing", "", "", "", 10)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := store.GetMessages(sessionKey, 10)
		if len(msgs) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	msgs, _ := store.GetMessages(sessionKey, 10)
	if len(msgs) < 1 {
		t.Fatalf("expected sub-agent to record an assistant message, got %d msgs", len(msgs))
	}

	last := msgs[len(msgs)-1]
	if !strings.HasPrefix(last.Content, "Error:") {
		t.Errorf("expected error-path assistant message (Error: …), got %q", last.Content)
	}
	if strings.Contains(last.Content, ai.EmptyResponseFallbackContent()[:40]) {
		t.Errorf("empty-guard fallback must not be delivered as a completed result, got %q", last.Content)
	}
}

func TestSubAgent_NormalCompletion_StillWorks(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.ctx = context.Background()
	gw.ai = subAgentFallbackRouter(t, []ai.MockResponse{
		{Content: "The task is done. Report follows."},
	})

	sessionKey, err := gw.SpawnSubAgent(context.Background(), "do the thing", "", "", "", 10)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := store.GetMessages(sessionKey, 10)
		if len(msgs) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	msgs, _ := store.GetMessages(sessionKey, 10)
	if len(msgs) < 1 {
		t.Fatalf("expected sub-agent to record a result message, got %d msgs", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if strings.Contains(last.Content, "Error:") {
		t.Errorf("normal completion must not error, got %q", last.Content)
	}
}
