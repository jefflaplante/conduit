package ai

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/config"
)

// conduit-31jg.79: tool-loop rounds (depthN) go through contextGuardProvider,
// which fell back on quota errors but not on timeouts — a timed-out depthN
// call killed the sub-agent ("AI response after tool execution failed: ...
// context deadline exceeded"). The guard now runs callWithRecovery's
// timeout step: one same-route retry within the deadline budget, then ONE
// sticky handoff to distinctFallbackRoute. Calls the EmptyGuard makes are
// owned by the EmptyGuard, so the fallback route is never tried twice for
// one call.

// guardedToolLoopEngine mimics tools.ExecutionEngine's post-tool rounds:
// each round calls the provider it was handed, then runs the result
// through GuardEmptyResponse (execution.go "depthN").
type guardedToolLoopEngine struct{ rounds int }

func (e *guardedToolLoopEngine) HandleToolCallFlow(ctx context.Context, provider Provider, req *GenerateRequest, resp *GenerateResponse) (ConversationResponse, error) {
	total := resp.Usage
	for i := 1; i <= e.rounds; i++ {
		r, err := provider.GenerateResponse(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("AI response after tool execution failed: %w", err)
		}
		r, err = GuardEmptyResponse(ctx, provider, req, r, err, fmt.Sprintf("depth%d", i))
		if err != nil {
			return nil, err
		}
		total.Add(r.Usage)
		resp = r
	}
	return &SimpleConversationResponse{Content: resp.Content, Usage: &total, Steps: e.rounds + 1}, nil
}

const tlModel = "claude-haiku-4-5-20251001"

// newToolLoopTimeoutRouter: "primary" (anthropic-like) falls back to
// "fallbackprov/fb-model-x"; the router is also the EmptyGuard's failover
// source, as in production.
func newToolLoopTimeoutRouter(t *testing.T, rounds int, primary, fallback Provider) (*Router, *countingObserver) {
	t.Helper()
	r, err := NewRouterWithExecution(config.AIConfig{}, nil, &guardedToolLoopEngine{rounds: rounds})
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterProvider("primary", primary)
	r.RegisterProvider("fallbackprov", fallback)
	r.mu.Lock()
	r.providerMeta["primary"] = ProviderMeta{Name: "primary", Type: "anthropic", DefaultModel: tlModel, FallbackModel: handoffFBModel}
	r.providerMeta["fallbackprov"] = ProviderMeta{Name: "fallbackprov", Type: "openai", DefaultModel: "fb-model-x"}
	r.mu.Unlock()
	withFailoverStub(t, r)
	obs := &countingObserver{}
	r.GetUsageTracker().SetObserver(obs)
	return r, obs
}

func runToolTurn(t *testing.T, r *Router, ctx context.Context, streaming bool) (string, error) {
	t.Helper()
	var resp ConversationResponse
	var err error
	if streaming {
		resp, err = r.GenerateResponseStreaming(ctx, newFallbackSession(t), "go", "primary", tlModel, func(string, bool) {})
	} else {
		resp, err = r.GenerateResponseWithToolsAndProgress(ctx, newFallbackSession(t), "go", "primary", tlModel, nil)
	}
	if err != nil {
		return "", err
	}
	return resp.GetContent(), nil
}

var tlToolCall = []ToolCall{{ID: "t1", Name: "Bash", Args: map[string]interface{}{}}}

func assertMetered(t *testing.T, obs *countingObserver, errs int, byModel map[string]int) {
	t.Helper()
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if obs.errors != errs {
		t.Errorf("metered errors = %d, want %d", obs.errors, errs)
	}
	for k, want := range byModel {
		if got := obs.models[k]; got != want {
			t.Errorf("metered %s = %d (all %v), want %d", k, got, obs.models, want)
		}
	}
	total := 0
	for _, n := range obs.models {
		total += n
	}
	wantTotal := 0
	for _, n := range byModel {
		wantTotal += n
	}
	if total != wantTotal {
		t.Errorf("metered successful calls = %d (all %v), want %d", total, obs.models, wantTotal)
	}
}

func TestToolLoopTimeout_RetriesOnceThenHandsOffOnceAndSticks(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-streaming", true: "streaming"}[streaming], func(t *testing.T) {
			primary := NewMockProvider("primary")
			fallback := NewMockProvider("fallbackprov")
			r, obs := newToolLoopTimeoutRouter(t, 2, primary, fallback)
			primary.SetResponses([]MockResponse{
				{ToolCalls: tlToolCall, Usage: Usage{PromptTokens: 10}}, // first call (callWithRecovery)
				{Error: errFallbackTimeout},                             // depth1
				{Error: errFallbackTimeout},                             // depth1 same-route retry
				{Content: "WRONG: primary used after the handoff"},
			})
			fallback.SetResponses([]MockResponse{
				{ToolCalls: tlToolCall, Usage: Usage{PromptTokens: 20}},     // depth1 handoff
				{Content: "fallback final", Usage: Usage{PromptTokens: 30}}, // depth2, sticky
				{Content: "WRONG: extra fallback call"},
			})

			content, err := runToolTurn(t, r, context.Background(), streaming)
			if err != nil {
				t.Fatalf("depth1 timeout killed the turn: %v", err)
			}
			if content != "fallback final" {
				t.Errorf("content = %q, want %q", content, "fallback final")
			}
			if n := primary.GetCallCount(); n != 3 {
				t.Errorf("primary calls = %d, want 3 (first call + depth1 + one retry)", n)
			}
			if n := fallback.GetCallCount(); n != 2 {
				t.Errorf("fallback calls = %d, want 2 (one handoff + sticky depth2)", n)
			}
			for i, c := range fallback.GetCalls() {
				if c.Request.Model != handoffFBModel {
					t.Errorf("fallback call %d model = %q, want %q", i, c.Request.Model, handoffFBModel)
				}
			}
			assertNoFallbackModelOnPrimary(t, primary, handoffFBModel)
			assertMetered(t, obs, 2, map[string]int{
				"primary|" + tlModel:             1,
				"fallbackprov|" + handoffFBModel: 2,
			})
		})
	}
}

func TestToolLoopTimeout_RetrySucceedsNoHandoff(t *testing.T) {
	primary := NewMockProvider("primary")
	fallback := NewMockProvider("fallbackprov")
	r, obs := newToolLoopTimeoutRouter(t, 2, primary, fallback)
	primary.SetResponses([]MockResponse{
		{ToolCalls: tlToolCall},
		{Error: errFallbackTimeout},
		{ToolCalls: tlToolCall}, // retry succeeds
		{Content: "primary final"},
	})
	content, err := runToolTurn(t, r, context.Background(), false)
	if err != nil || content != "primary final" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if n := fallback.GetCallCount(); n != 0 {
		t.Errorf("fallback calls = %d, want 0", n)
	}
	assertMetered(t, obs, 1, map[string]int{"primary|" + tlModel: 3})
}

func TestToolLoopTimeout_HandoffKeepsDeadlineReserve(t *testing.T) {
	shrinkRecoveryBudget(t, 20*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond)
	primary := &ctxProvider{name: "primary", responses: []MockResponse{
		{ToolCalls: tlToolCall},
		{Error: errFallbackTimeout}, // depth1
		{Error: errHang},            // retry hangs until its carved deadline
	}}
	fallback := &ctxProvider{name: "fallbackprov", responses: []MockResponse{{Content: "fallback in time"}}}
	r, obs := newToolLoopTimeoutRouter(t, 1, primary, fallback)

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	content, err := runToolTurn(t, r, ctx, false)
	if err != nil || content != "fallback in time" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if n := primary.callCount(); n != 3 {
		t.Errorf("primary calls = %d, want 3", n)
	}
	if n := fallback.callCount(); n != 1 {
		t.Fatalf("fallback calls = %d, want 1", n)
	}
	if left := primary.leftAt(2); left > 650*time.Millisecond {
		t.Errorf("retry got %s, want its deadline carved to leave the 300ms reserve", left)
	}
	if left := fallback.leftAt(0); left < 200*time.Millisecond {
		t.Errorf("handoff started with %s left, want >= ~300ms reserve", left)
	}
	assertMetered(t, obs, 2, map[string]int{
		"primary|" + tlModel:             1,
		"fallbackprov|" + handoffFBModel: 1,
	})
}

func TestToolLoopTimeout_SkipsRetryWhenBudgetOnlyCoversHandoff(t *testing.T) {
	shrinkRecoveryBudget(t, 20*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond)
	primary := &ctxProvider{name: "primary", responses: []MockResponse{
		{ToolCalls: tlToolCall},
		{Error: errFallbackTimeout},
		{Content: "WRONG: retried with no budget"},
	}}
	fallback := &ctxProvider{name: "fallbackprov", responses: []MockResponse{{Content: "fallback in time"}}}
	r, _ := newToolLoopTimeoutRouter(t, 1, primary, fallback)

	// 350ms: reserve 300ms leaves < minRetry 100ms → straight to the handoff.
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	content, err := runToolTurn(t, r, ctx, false)
	if err != nil || content != "fallback in time" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if n := primary.callCount(); n != 2 {
		t.Errorf("primary calls = %d, want 2 (retry skipped)", n)
	}
	if n := fallback.callCount(); n != 1 {
		t.Errorf("fallback calls = %d, want 1", n)
	}
}

func TestToolLoopTimeout_NoHandoffWithoutAttemptBudget(t *testing.T) {
	// minAttempt 500ms > what's left after the carved retry → no handoff.
	shrinkRecoveryBudget(t, 500*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond)
	primary := &ctxProvider{name: "primary", responses: []MockResponse{
		{ToolCalls: tlToolCall},
		{Error: errFallbackTimeout},
		{Error: errHang},
	}}
	fallback := &ctxProvider{name: "fallbackprov", responses: []MockResponse{{Content: "WRONG: doomed handoff started"}}}
	r, _ := newToolLoopTimeoutRouter(t, 1, primary, fallback)

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if _, err := runToolTurn(t, r, ctx, false); err == nil {
		t.Fatal("expected the timeout to surface")
	}
	if n := fallback.callCount(); n != 0 {
		t.Errorf("fallback calls = %d, want 0 (no budget for an attempt)", n)
	}
}

func TestToolLoopTimeout_NoHandoffWhenFallbackIsSameRoute(t *testing.T) {
	primary := NewMockProvider("primary")
	fallback := NewMockProvider("fallbackprov")
	r, obs := newToolLoopTimeoutRouter(t, 1, primary, fallback)
	r.mu.Lock()
	meta := r.providerMeta["primary"]
	meta.FallbackModel = "primary/" + tlModel // == the route's own model
	r.providerMeta["primary"] = meta
	r.mu.Unlock()
	primary.SetResponses([]MockResponse{
		{ToolCalls: tlToolCall},
		{Error: errFallbackTimeout},
		{Error: errFallbackTimeout},
		{Content: "WRONG: pointless third attempt"},
	})

	if _, err := runToolTurn(t, r, context.Background(), false); err == nil {
		t.Fatal("expected the timeout to surface")
	}
	if n := primary.GetCallCount(); n != 3 {
		t.Errorf("primary calls = %d, want 3 (first + depth1 + one retry, no same-route 'handoff')", n)
	}
	if n := fallback.GetCallCount(); n != 0 {
		t.Errorf("fallback calls = %d, want 0", n)
	}
	assertMetered(t, obs, 2, map[string]int{"primary|" + tlModel: 1})
}

// A timeout in the EmptyGuard's same-model retry belongs to the EmptyGuard:
// the context guard neither retries it nor hands off, and the EmptyGuard's
// failover is the only call to the fallback route.
func TestToolLoopTimeout_InsideEmptyGuardRetryHandsOffOnlyOnce(t *testing.T) {
	primary := NewMockProvider("primary")
	fallback := NewMockProvider("fallbackprov")
	r, obs := newToolLoopTimeoutRouter(t, 1, primary, fallback)
	primary.SetResponses([]MockResponse{
		{ToolCalls: tlToolCall},
		{Content: ""},               // depth1: raw empty → EmptyGuard
		{Error: errFallbackTimeout}, // EmptyGuard retry times out
		{Content: "WRONG: context guard retried the EmptyGuard's call"},
	})
	fallback.SetResponses([]MockResponse{
		{Content: "recovered by failover"},
		{Content: "WRONG: fallback route tried twice"},
	})

	content, err := runToolTurn(t, r, context.Background(), false)
	if err != nil || content != "recovered by failover" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if n := primary.GetCallCount(); n != 3 {
		t.Errorf("primary calls = %d, want 3 (first + empty depth1 + EmptyGuard retry)", n)
	}
	if n := fallback.GetCallCount(); n != 1 {
		t.Errorf("fallback calls = %d, want exactly 1 (EmptyGuard failover only)", n)
	}
	assertMetered(t, obs, 1, map[string]int{
		"primary|" + tlModel:             2,
		"fallbackprov|" + handoffFBModel: 1,
	})
}

// After a timeout handoff the guard serves the fallback model; an empty from
// it must not "fail over" to that same fallback route again (owner's
// z-ai fallback_model z-ai/glm-5.3 while already handed off to glm-5.3).
func TestToolLoopTimeout_EmptyAfterHandoffDoesNotRetargetFallback(t *testing.T) {
	primary := NewMockProvider("primary")
	fallback := NewMockProvider("fallbackprov")
	r, obs := newToolLoopTimeoutRouter(t, 1, primary, fallback)
	r.mu.Lock()
	fm := r.providerMeta["fallbackprov"]
	fm.FallbackModel = handoffFBModel // its own model
	r.providerMeta["fallbackprov"] = fm
	r.mu.Unlock()
	primary.SetResponses([]MockResponse{
		{ToolCalls: tlToolCall},
		{Error: errFallbackTimeout},
		{Error: errFallbackTimeout},
	})
	fallback.SetResponses([]MockResponse{
		{Content: ""}, // handoff: raw empty → EmptyGuard
		{Content: ""}, // EmptyGuard same-model retry on the sticky route
		{Content: "WRONG: fallback route re-targeted as failover"},
	})

	content, err := runToolTurn(t, r, context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !IsEmptyResponseFallback(content) {
		t.Errorf("content = %q, want the visible empty fallback", content)
	}
	if n := fallback.GetCallCount(); n != 2 {
		t.Errorf("fallback calls = %d, want 2 (handoff + EmptyGuard retry, no failover to itself)", n)
	}
	assertMetered(t, obs, 2, map[string]int{
		"primary|" + tlModel:             1,
		"fallbackprov|" + handoffFBModel: 2,
	})
}

// At most one route change per turn: when callWithRecovery already handed
// the first call off, a later tool-round timeout retries the fallback route
// but never moves to the fallback's own fallback.
func TestToolLoopTimeout_NoSecondMoveAfterFirstCallHandoff(t *testing.T) {
	primary := NewMockProvider("primary")
	fallback := NewMockProvider("fallbackprov")
	third := NewMockProvider("third")
	r, _ := newToolLoopTimeoutRouter(t, 1, primary, fallback)
	r.RegisterProvider("third", third)
	r.mu.Lock()
	r.providerMeta["third"] = ProviderMeta{Name: "third", Type: "openai", DefaultModel: "t"}
	fm := r.providerMeta["fallbackprov"]
	fm.FallbackModel = "third/t"
	r.providerMeta["fallbackprov"] = fm
	r.mu.Unlock()
	primary.SetResponses([]MockResponse{{Error: errFallbackTimeout}, {Error: errFallbackTimeout}})
	fallback.SetResponses([]MockResponse{
		{ToolCalls: tlToolCall},     // first call, handed off by callWithRecovery
		{Error: errFallbackTimeout}, // depth1
		{Error: errFallbackTimeout}, // depth1 same-route retry
	})
	third.SetResponses([]MockResponse{{Content: "WRONG: second route change"}})

	if _, err := runToolTurn(t, r, context.Background(), false); err == nil {
		t.Fatal("expected the timeout to surface")
	}
	if n := fallback.GetCallCount(); n != 3 {
		t.Errorf("fallback calls = %d, want 3", n)
	}
	if n := third.GetCallCount(); n != 0 {
		t.Errorf("third-route calls = %d, want 0", n)
	}
}

// The EmptyGuard failover guard never recovers on a timeout either.
func TestEmptyFailoverGuard_DoesNotRetryTimeout(t *testing.T) {
	primary := NewMockProvider("primary")
	fallback := NewMockProvider("fallbackprov")
	r, _ := newToolLoopTimeoutRouter(t, 0, primary, fallback)
	fallback.SetResponses([]MockResponse{{Error: errFallbackTimeout}, {Content: "WRONG: failover retried"}})
	_, p, ok := r.ResolveEmptyFailover("primary")
	if !ok {
		t.Fatal("expected a failover route")
	}
	if _, err := p.GenerateResponse(context.Background(), &GenerateRequest{Model: handoffFBModel}); err == nil {
		t.Fatal("expected the timeout to surface")
	}
	if n := fallback.GetCallCount(); n != 1 {
		t.Errorf("failover calls = %d, want 1", n)
	}
}
