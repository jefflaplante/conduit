package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// conduit-18vj: raw-empty model responses must never complete a turn silently.
// Retry once, then deliver a visible fallback.
//
// NOTE: in retry tests the ORIGINAL empty response is a literal argument to
// GuardEmptyResponse — only the RETRY call hits the mock provider. Mock
// responses therefore describe the retry only.

func TestGuardEmptyResponse_PassthroughNonEmpty(t *testing.T) {
	p := NewMockProvider("test")

	resp, err := GuardEmptyResponse(context.Background(), p,
		&GenerateRequest{}, &GenerateResponse{Content: "hello"}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("expected passthrough, got %q", resp.Content)
	}
	if len(p.calls) != 0 {
		t.Fatalf("non-empty passthrough must not call provider, got %d calls", len(p.calls))
	}
}

func TestGuardEmptyResponse_ToolCallsAreNotEmpty(t *testing.T) {
	p := NewMockProvider("test")

	resp, err := GuardEmptyResponse(context.Background(), p,
		&GenerateRequest{}, &GenerateResponse{ToolCalls: []ToolCall{{Name: "Bash"}}}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected tool calls passthrough")
	}
	if len(p.calls) != 0 {
		t.Fatalf("tool-call response must not trigger retry, got %d calls", len(p.calls))
	}
}

func TestGuardEmptyResponse_RetryRecovers(t *testing.T) {
	p := NewMockProvider("test")
	p.SetResponses([]MockResponse{
		{Content: "recovered"}, // returned by the RETRY call
	})

	resp, err := GuardEmptyResponse(context.Background(), p,
		&GenerateRequest{}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "recovered" {
		t.Fatalf("expected retry content, got %q", resp.Content)
	}
	if len(p.calls) != 1 {
		t.Fatalf("expected exactly 1 provider call (the retry), got %d", len(p.calls))
	}
}

func TestGuardEmptyResponse_RetryAlsoEmptyDeliversFallback(t *testing.T) {
	p := NewMockProvider("test")
	p.SetResponses([]MockResponse{
		{Content: "   "}, // retry ALSO empty (whitespace-only counts as empty)
	})

	resp, err := GuardEmptyResponse(context.Background(), p,
		&GenerateRequest{}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("guard must not error on double-empty: %v", err)
	}
	if resp.Content == "" {
		t.Fatal("fallback content must never be empty (conduit-18vj guarantee)")
	}
	if !strings.Contains(resp.Content, "empty response") {
		t.Fatalf("fallback should explain the empty-response condition, got %q", resp.Content)
	}
	if len(p.calls) != 1 {
		t.Fatalf("expected 1 provider call (the retry), got %d", len(p.calls))
	}
}

func TestGuardEmptyResponse_GenErrorPassthrough(t *testing.T) {
	p := NewMockProvider("test")
	boom := errors.New("provider down")

	_, err := GuardEmptyResponse(context.Background(), p,
		&GenerateRequest{}, nil, boom, "test")
	if !errors.Is(err, boom) {
		t.Fatalf("generation errors must pass through untouched, got %v", err)
	}
	if len(p.calls) != 0 {
		t.Fatalf("errors are not this guard's job — expected no retry calls, got %d", len(p.calls))
	}
}

func TestIsEmptyModelResponse_NilAndWhitespace(t *testing.T) {
	if !IsEmptyModelResponse(nil) {
		t.Fatal("nil response must count as empty")
	}
	if !IsEmptyModelResponse(&GenerateResponse{Content: "  \n"}) {
		t.Fatal("whitespace-only content must count as empty")
	}
	if IsEmptyModelResponse(&GenerateResponse{Content: "x", ToolCalls: []ToolCall{{Name: "t"}}}) {
		t.Fatal("tool calls must count as non-empty")
	}
}
