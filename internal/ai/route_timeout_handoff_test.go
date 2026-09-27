package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// conduit-1w48: when the bd-13p timeout retry on route A is exhausted, the
// turn hands off to A's fallback route — exactly once, deadline-aware, and
// every attempt is metered exactly once (conduit-31jg.64).

const handoffFBModel = "fallbackprov/fb-model-x"

func handoffPaths(t *testing.T) map[string]func(r *Router, ctx context.Context) (string, error) {
	return map[string]func(r *Router, ctx context.Context) (string, error){
		"generate": func(r *Router, ctx context.Context) (string, error) {
			resp, err := r.GenerateResponse(ctx, newFallbackSession(t), "hi", "primary")
			if err != nil {
				return "", err
			}
			return resp.Content, nil
		},
		"tools": func(r *Router, ctx context.Context) (string, error) {
			resp, err := r.GenerateResponseWithToolsAndProgress(ctx, newFallbackSession(t), "hi", "primary", "claude-haiku-4-5-20251001", nil)
			if err != nil {
				return "", err
			}
			return resp.GetContent(), nil
		},
		"streaming": func(r *Router, ctx context.Context) (string, error) {
			resp, err := r.GenerateResponseStreaming(ctx, newFallbackSession(t), "hi", "primary", "claude-haiku-4-5-20251001", func(string, bool) {})
			if err != nil {
				return "", err
			}
			return resp.GetContent(), nil
		},
	}
}

func TestTimeoutRetryExhausted_HandsOffToFallbackExactlyOnce(t *testing.T) {
	for name, run := range handoffPaths(t) {
		t.Run(name, func(t *testing.T) {
			router, primary, fallback := newFallbackTestRouter(t)
			obs := &countingObserver{}
			router.GetUsageTracker().SetObserver(obs)
			primary.SetResponses([]MockResponse{{Error: errFallbackTimeout}, {Error: errFallbackTimeout}, {Content: "WRONG: third primary call"}})
			fallback.SetResponses([]MockResponse{{Content: "fallback served", Usage: Usage{PromptTokens: 10, CompletionTokens: 2}}, {Content: "WRONG: second fallback call"}})

			content, err := run(router, context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if content != "fallback served" {
				t.Errorf("content = %q, want %q", content, "fallback served")
			}
			if n := primary.GetCallCount(); n != 2 {
				t.Errorf("primary calls = %d, want 2 (original + bd-13p retry)", n)
			}
			if n := fallback.GetCallCount(); n != 1 {
				t.Fatalf("fallback calls = %d, want exactly 1", n)
			}
			if m := fallback.GetCalls()[0].Request.Model; m != handoffFBModel {
				t.Errorf("fallback model = %q, want %q", m, handoffFBModel)
			}
			assertNoFallbackModelOnPrimary(t, primary, handoffFBModel)
			// Metering: 2 failed primary attempts + 1 served fallback call.
			obs.mu.Lock()
			defer obs.mu.Unlock()
			if obs.errors != 2 {
				t.Errorf("metered errors = %d, want 2", obs.errors)
			}
			if got := obs.models["fallbackprov|"+handoffFBModel]; got != 1 {
				t.Errorf("metered fallback calls = %d (all: %v), want 1", got, obs.models)
			}
		})
	}
}

func TestTimeoutHandoff_FallbackTimeoutIsNotRetriedOrHandedOn(t *testing.T) {
	router, primary, fallback := newFallbackTestRouter(t)
	primary.SetResponses([]MockResponse{{Error: errFallbackTimeout}, {Error: errFallbackTimeout}})
	fallback.SetResponses([]MockResponse{{Error: errFallbackTimeout}, {Content: "WRONG: fallback retried"}})

	_, err := router.GenerateResponse(context.Background(), newFallbackSession(t), "hi", "primary")
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected the fallback's timeout error, got %v", err)
	}
	if n := primary.GetCallCount(); n != 2 {
		t.Errorf("primary calls = %d, want 2", n)
	}
	if n := fallback.GetCallCount(); n != 1 {
		t.Errorf("fallback calls = %d, want 1 (no double retry)", n)
	}
}

func TestTimeoutHandoff_SkippedWhenFallbackIsSameRoute(t *testing.T) {
	router, primary, fallback := newFallbackTestRouter(t)
	router.mu.Lock()
	meta := router.providerMeta["primary"]
	meta.FallbackModel = "primary/claude-haiku-4-5-20251001" // == primary's default model
	router.providerMeta["primary"] = meta
	router.mu.Unlock()
	primary.SetResponses([]MockResponse{{Error: errFallbackTimeout}, {Error: errFallbackTimeout}, {Content: "WRONG: pointless third attempt"}})

	_, err := router.GenerateResponse(context.Background(), newFallbackSession(t), "hi", "primary")
	if err == nil {
		t.Fatal("expected the timeout error to surface")
	}
	if n := primary.GetCallCount(); n != 2 {
		t.Errorf("primary calls = %d, want 2 (same-route 'fallback' is not a handoff)", n)
	}
	if n := fallback.GetCallCount(); n != 0 {
		t.Errorf("fallback provider calls = %d, want 0", n)
	}
}

func TestTimeoutHandoff_FallbackGetsReservedSliceNearDeadline(t *testing.T) {
	shrinkRecoveryBudget(t, 20*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond)
	router, _, _ := newFallbackTestRouter(t)
	primary := &ctxProvider{name: "primary", responses: []MockResponse{{Error: errFallbackTimeout}, {Error: errHang}}}
	fallback := &ctxProvider{name: "fallbackprov", responses: []MockResponse{{Content: "fallback in time"}}}
	router.RegisterProvider("primary", primary)
	router.RegisterProvider("fallbackprov", fallback)

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	resp, err := router.GenerateResponse(ctx, newFallbackSession(t), "hi", "primary")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "fallback in time" {
		t.Errorf("content = %q", resp.Content)
	}
	if n := primary.callCount(); n != 2 {
		t.Errorf("primary calls = %d, want 2", n)
	}
	if n := fallback.callCount(); n != 1 {
		t.Fatalf("fallback calls = %d, want 1", n)
	}
	if left := fallback.leftAt(0); left < 200*time.Millisecond {
		t.Errorf("fallback started with %s left, want >= ~300ms reserve", left)
	}
}

func TestTimeoutHandoff_SkipsSameRouteRetryWhenBudgetOnlyCoversFallback(t *testing.T) {
	shrinkRecoveryBudget(t, 20*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond)
	router, _, _ := newFallbackTestRouter(t)
	primary := &ctxProvider{name: "primary", responses: []MockResponse{{Error: errFallbackTimeout}, {Content: "WRONG: retried with no budget"}}}
	fallback := &ctxProvider{name: "fallbackprov", responses: []MockResponse{{Content: "fallback in time"}}}
	router.RegisterProvider("primary", primary)
	router.RegisterProvider("fallbackprov", fallback)

	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	resp, err := router.GenerateResponse(ctx, newFallbackSession(t), "hi", "primary")
	if err != nil || resp.Content != "fallback in time" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if n := primary.callCount(); n != 1 {
		t.Errorf("primary calls = %d, want 1 (retry skipped, fallback has the better chance)", n)
	}
}

func TestTimeoutRetry_NoFallbackRouteKeepsSingleRetry(t *testing.T) {
	router, primary, fallback := newFallbackTestRouter(t)
	router.mu.Lock()
	meta := router.providerMeta["primary"]
	meta.FallbackModel = "nowhere/unknown-model"
	router.providerMeta["primary"] = meta
	router.mu.Unlock()
	primary.SetResponses([]MockResponse{{Error: errFallbackTimeout}, {Error: fmt.Errorf("provider call: %w", context.DeadlineExceeded)}})

	_, err := router.GenerateResponse(context.Background(), newFallbackSession(t), "hi", "primary")
	if err == nil {
		t.Fatal("expected error")
	}
	if n := primary.GetCallCount(); n != 2 {
		t.Errorf("primary calls = %d, want 2", n)
	}
	if n := fallback.GetCallCount(); n != 0 {
		t.Errorf("fallback calls = %d, want 0", n)
	}
}
