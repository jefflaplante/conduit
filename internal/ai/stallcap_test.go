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
