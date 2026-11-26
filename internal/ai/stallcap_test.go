package ai

import (
	"context"
	"strings"
	"testing"
)

// glm-5.4 stall-cap (sd-glm54-stallcap-2026-11-26):
//
// glm-5.4 truncation mid-reasoning returns finish_reason=length with ZERO
// content ("raw-empty"). The old behavior multiplied the cost:
//
//  1. GuardEmptyResponse treated it like a load-induced empty and did a
//     same-model retry — proven futile (content 0/1 in the incident; the
//     model deterministically re-exhausts its budget on invisible reasoning
//     tokens). Only the cross-model failover produced content (3/3).
//  2. ContinueLengthTruncated handed a failover-produced fragment to the
//     REQUEST's original model, re-igniting the stall spiral (9 capped calls
//     / ~284K tokens / ~48 min for one tool round).
//
// New contract:
//   - length-truncated raw-empty responses SKIP the same-model retry and go
//     straight to the cross-model failover (deterministic reasoning
//     exhaustion is not load-induced emptiness).
//   - continuations go to the model that PRODUCED the fragment (sticky),
//     not the request's original model.
//
// NOTE (same convention as empty_guard_test.go): in retry tests the ORIGINAL
// empty response is a literal argument to GuardEmptyResponse — only the
// RETRY and FAILOVER calls hit mock providers.

// --- Change 1: guard skip on length-truncated raw-empty ---

func TestGuardEmptyResponse_LengthEmptySkipsRetry(t *testing.T) {
	sameModel := NewMockProvider("z-ai")
	failover := NewMockProvider("anthropic")
	failover.SetResponses([]MockResponse{
		{Content: "recovered via failover"},
	})
	stub := &failoverRouterStub{model: "claude-sonnet-4-6", provider: failover, ok: true}
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "glm-5.4"},
		&GenerateResponse{FinishReason: "length"}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "recovered via failover" {
		t.Fatalf("expected failover content, got %q", resp.Content)
	}
	if resp.ServedBy != "claude-sonnet-4-6" {
		t.Fatalf("failover response must carry the model that produced it, got ServedBy %q", resp.ServedBy)
	}
	if calls := sameModel.GetCallCount(); calls != 0 {
		t.Fatalf("length-truncated raw-empty must skip the same-model retry, got %d retry calls", calls)
	}
	if calls := failover.GetCallCount(); calls != 1 {
		t.Fatalf("expected exactly 1 failover call, got %d", calls)
	}
}

func TestGuardEmptyResponse_LengthEmptyNoRouteDeliversFallback(t *testing.T) {
	p := NewMockProvider("z-ai")
	stub := &failoverRouterStub{ok: false}
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	resp, err := GuardEmptyResponse(context.Background(), p,
		&GenerateRequest{Model: "glm-5.4"},
		&GenerateResponse{FinishReason: "length"}, nil, "test")
	if err != nil {
		t.Fatalf("guard must not error when no failover route exists: %v", err)
	}
	if !strings.Contains(resp.Content, "empty response") {
		t.Fatalf("expected visible fallback, got %q", resp.Content)
	}
	if calls := p.GetCallCount(); calls != 0 {
		t.Fatalf("no-route length-empty must not call the provider at all, got %d calls", calls)
	}
}

func TestGuardEmptyResponse_LengthEmptyWithContentStillContinues(t *testing.T) {
	// finish_reason=length WITH content is a truncation, not a raw-empty —
	// the guard passes it through untouched (the length auto-continue owns it).
	p := NewMockProvider("test")

	resp, err := GuardEmptyResponse(context.Background(), p,
		&GenerateRequest{Model: "glm-5.4"},
		&GenerateResponse{Content: "partial answer", FinishReason: "length"}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "partial answer" {
		t.Fatalf("expected passthrough, got %q", resp.Content)
	}
	if calls := p.GetCallCount(); calls != 0 {
		t.Fatalf("content-bearing length response must not trigger the guard, got %d calls", calls)
	}
}

func TestGuardEmptyResponse_EmptyWithoutLengthStillRetries(t *testing.T) {
	// Load-induced emptiness (no finish_reason=length) keeps the existing
	// same-model retry — the skip is specific to length-truncated empties.
	// The retry must run even when the guard skip path exists.
	p := NewMockProvider("test")
	p.SetResponses([]MockResponse{
		{Content: "recovered"},
	})

	resp, err := GuardEmptyResponse(context.Background(), p,
		&GenerateRequest{Model: "glm-5.4"},
		&GenerateResponse{FinishReason: "stop", Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "recovered" {
		t.Fatalf("expected retry content, got %q", resp.Content)
	}
	if calls := p.GetCallCount(); calls != 1 {
		t.Fatalf("expected exactly 1 provider call (the retry), got %d", calls)
	}
}

// --- Change 2: sticky continuation model ---

func TestContinueLengthTruncated_StickyToFragmentModel(t *testing.T) {
	// A failover-produced fragment (served by glm-5.3) must be continued on
	// glm-5.3, not the request's original model glm-5.4.
	p := NewMockProvider("z-ai")
	p.SetResponses([]MockResponse{
		{Content: " continued", FinishReason: "stop"},
	})
	req := &GenerateRequest{
		Model:    "glm-5.4",
		Messages: []ChatMessage{{Role: "user", Content: "question"}},
	}
	resp := &GenerateResponse{
		Content:      "partial fragment",
		FinishReason: "length",
		ServedBy:     "glm-5.3",
	}

	out := ContinueLengthTruncated(context.Background(), p, req, resp, "test")
	if !strings.Contains(out.Content, "partial fragment") || !strings.Contains(out.Content, " continued") {
		t.Fatalf("expected concatenated fragments, got %q", out.Content)
	}
	calls := p.GetCalls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 continuation call, got %d", len(calls))
	}
	if got := calls[0].Request.Model; got != "glm-5.3" {
		t.Fatalf("continuation must go to the fragment's model (sticky), got %q", got)
	}
	if calls[0].Request.Messages[len(calls[0].Request.Messages)-1].Content != "continue" {
		t.Fatal("expected the synthetic continue prompt as the last message")
	}
}

func TestContinueLengthTruncated_NoServedByUsesRequestModel(t *testing.T) {
	// No ServedBy (in-process provider call, the common path) — behavior
	// unchanged: continuation on req.Model.
	p := NewMockProvider("z-ai")
	p.SetResponses([]MockResponse{
		{Content: " continued", FinishReason: "stop"},
	})
	req := &GenerateRequest{
		Model:    "glm-5.4",
		Messages: []ChatMessage{{Role: "user", Content: "question"}},
	}
	resp := &GenerateResponse{
		Content:      "partial fragment",
		FinishReason: "length",
	}

	out := ContinueLengthTruncated(context.Background(), p, req, resp, "test")
	if !strings.Contains(out.Content, "partial fragment") || !strings.Contains(out.Content, " continued") {
		t.Fatalf("expected concatenated fragments, got %q", out.Content)
	}
	calls := p.GetCalls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 continuation call, got %d", len(calls))
	}
	if got := calls[0].Request.Model; got != "glm-5.4" {
		t.Fatalf("continuation without ServedBy must use req.Model, got %q", got)
	}
}

func TestContinueLengthTruncated_StickyPersistsAcrossContinues(t *testing.T) {
	// Every continuation in the loop stays on the fragment's model.
	p := NewMockProvider("z-ai")
	p.SetResponses([]MockResponse{
		{Content: " more1", FinishReason: "length", ServedBy: "glm-5.3"},
		{Content: " more2", FinishReason: "stop", ServedBy: "glm-5.3"},
	})
	req := &GenerateRequest{
		Model:    "glm-5.4",
		Messages: []ChatMessage{{Role: "user", Content: "question"}},
	}
	resp := &GenerateResponse{
		Content:      "fragment0",
		FinishReason: "length",
		ServedBy:     "glm-5.3",
	}

	out := ContinueLengthTruncated(context.Background(), p, req, resp, "test")
	if !strings.Contains(out.Content, "fragment0") || !strings.Contains(out.Content, " more1") || !strings.Contains(out.Content, " more2") {
		t.Fatalf("expected all fragments concatenated, got %q", out.Content)
	}
	calls := p.GetCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 continuation calls, got %d", len(calls))
	}
	for i, c := range calls {
		if got := c.Request.Model; got != "glm-5.3" {
			t.Fatalf("continuation %d must stay on the fragment model, got %q", i+1, got)
		}
	}
}

func TestContinueLengthTruncated_GuardFailoverCarriesServedBy(t *testing.T) {
	// A raw-empty continuation handed to GuardEmptyResponse: the guard must
	// receive the STICKY continuation request (fragment model), not the
	// original request. Discriminator: the stub's fallback model equals the
	// ORIGINAL request model. Same-model refusal compares against the guard's
	// req.Model — old behavior (guard sees glm-5.4) would refuse the
	// "failover" to glm-5.4; sticky behavior (guard sees glm-5.3) proceeds.
	// req.Model must also stay untouched for the caller's bookkeeping.
	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{
		{Content: "", FinishReason: "length"}, // continuation raw-empty + length -> guard skips retry
	})
	failover := NewMockProvider("anthropic")
	failover.SetResponses([]MockResponse{
		{Content: "failover continued fragment", FinishReason: "stop"},
	})
	stub := &failoverRouterStub{model: "glm-5.4", provider: failover, ok: true}
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	req := &GenerateRequest{
		Model:    "glm-5.4",
		Messages: []ChatMessage{{Role: "user", Content: "question"}},
	}
	resp := &GenerateResponse{
		Content:      "fragment0",
		FinishReason: "length",
		ServedBy:     "glm-5.3",
	}

	out := ContinueLengthTruncated(context.Background(), sameModel, req, resp, "test")
	// Raw continuation ran on the fragment's model (sticky).
	rawCalls := sameModel.GetCalls()
	if len(rawCalls) != 1 {
		t.Fatalf("expected 1 raw continuation call, got %d", len(rawCalls))
	}
	if got := rawCalls[0].Request.Model; got != "glm-5.3" {
		t.Fatalf("raw continuation must carry the fragment's model, got %q", got)
	}
	// The guard proceeded to failover — only possible because it saw the
	// sticky glm-5.3 as its req.Model (refusal would have suppressed it).
	failoverCalls := failover.GetCalls()
	if len(failoverCalls) != 1 {
		t.Fatalf("expected 1 guard failover call (same-model refusal must NOT fire), got %d", len(failoverCalls))
	}
	if got := failoverCalls[0].Request.Model; got != "glm-5.4" {
		t.Fatalf("failover attempt must carry the route's fallback model, got %q", got)
	}
	if req.Model != "glm-5.4" {
		t.Fatalf("req.Model must not be mutated, got %q", req.Model)
	}
	// Final assembly: fragment0 + failover continuation, complete (no marker).
	if !strings.Contains(out.Content, "fragment0") || !strings.Contains(out.Content, "failover continued fragment") {
		t.Fatalf("expected fragments joined, got %q", out.Content)
	}
	if strings.Contains(out.Content, lengthTruncatedMarker) {
		t.Fatalf("failover continuation ended the turn (finish_reason=stop) — no marker expected, got %q", out.Content)
	}
}
