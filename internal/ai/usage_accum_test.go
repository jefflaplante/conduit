package ai

import (
	"context"
	"testing"
)

// conduit-31jg.15: guard retries and auto-continues are billed; their usage
// must reach the response handed back to the caller.

func TestUsageAdd_SumsAllFieldsAndTracksLastContext(t *testing.T) {
	var u Usage
	u.Add(Usage{PromptTokens: 10, CompletionTokens: 5, CacheCreationInputTokens: 100, CacheReadInputTokens: 0})
	u.Add(Usage{PromptTokens: 3, CompletionTokens: 7, TotalTokens: 10, CacheReadInputTokens: 100})
	want := Usage{PromptTokens: 13, CompletionTokens: 12, TotalTokens: 25,
		CacheCreationInputTokens: 100, CacheReadInputTokens: 100, ContextTokens: 103}
	if u != want {
		t.Fatalf("got %+v\nwant %+v", u, want)
	}
	if u.Context() != 103 {
		t.Errorf("Context() = %d, want last call's 3+100", u.Context())
	}
	// A zero-usage response (local fallback) must not clobber the gauge.
	u.Add(Usage{})
	if u.Context() != 103 {
		t.Errorf("zero Add changed Context() to %d", u.Context())
	}
	if (Usage{PromptTokens: 5, CacheReadInputTokens: 7}).Context() != 12 {
		t.Errorf("single-call Context should be input + cache")
	}
}

func TestGuardEmptyResponse_FoldsRetryUsage(t *testing.T) {
	p := NewMockProvider("m")
	p.SetResponses([]MockResponse{{Content: "ok", Usage: Usage{PromptTokens: 20, CompletionTokens: 2, CacheReadInputTokens: 500}}})
	empty := &GenerateResponse{Usage: Usage{PromptTokens: 20, CacheReadInputTokens: 500}}
	got, err := GuardEmptyResponse(context.Background(), p, &GenerateRequest{}, empty, nil, "t")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "ok" || got.Usage.PromptTokens != 40 || got.Usage.CacheReadInputTokens != 1000 || got.Usage.CompletionTokens != 2 {
		t.Errorf("usage not folded: %+v", got.Usage)
	}
}

func TestGuardEmptyResponse_FallbackCarriesSpentUsage(t *testing.T) {
	prev := emptyFailoverRouter
	emptyFailoverRouter = nil
	t.Cleanup(func() { emptyFailoverRouter = prev })

	p := NewMockProvider("m")
	p.SetResponses([]MockResponse{{Content: "", Usage: Usage{PromptTokens: 30}}})
	empty := &GenerateResponse{Usage: Usage{PromptTokens: 30}}
	got, _ := GuardEmptyResponse(context.Background(), p, &GenerateRequest{}, empty, nil, "t")
	if !IsEmptyResponseFallback(got.Content) {
		t.Fatalf("expected fallback, got %q", got.Content)
	}
	if got.Usage.PromptTokens != 60 {
		t.Errorf("fallback usage = %+v, want both attempts (60)", got.Usage)
	}
}

func TestContinueLengthTruncated_SumsContinuations(t *testing.T) {
	p := NewMockProvider("m")
	p.SetResponses([]MockResponse{
		{Content: "b", FinishReason: "length", Usage: Usage{PromptTokens: 200, CompletionTokens: 100, CacheReadInputTokens: 50}},
		{Content: "c", FinishReason: "stop", Usage: Usage{PromptTokens: 300, CompletionTokens: 10, CacheReadInputTokens: 50}},
	})
	first := &GenerateResponse{Content: "a", FinishReason: "length", Usage: Usage{PromptTokens: 100, CompletionTokens: 100, CacheCreationInputTokens: 50}}
	got := ContinueLengthTruncated(context.Background(), p, &GenerateRequest{}, first, "t")
	if got.Content != "abc" {
		t.Fatalf("content = %q", got.Content)
	}
	u := got.Usage
	if u.PromptTokens != 600 || u.CompletionTokens != 210 || u.CacheCreationInputTokens != 50 || u.CacheReadInputTokens != 100 {
		t.Errorf("usage = %+v", u)
	}
	if u.Context() != 350 {
		t.Errorf("Context() = %d, want last call 300+50", u.Context())
	}
}

// TestContinueLengthTruncated_MarksContinueInjected covers conduit-31jg.87:
// the synthetic "continue" user turn is flagged Injected so goal extraction
// in the tool loop skips it.
func TestContinueLengthTruncated_MarksContinueInjected(t *testing.T) {
	p := NewMockProvider("m")
	p.SetResponses([]MockResponse{{Content: "b", FinishReason: "stop"}})
	req := &GenerateRequest{Messages: []ChatMessage{{Role: "user", Content: "write an essay"}}}
	first := &GenerateResponse{Content: "a", FinishReason: "length"}
	ContinueLengthTruncated(context.Background(), p, req, first, "t")
	if len(req.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(req.Messages))
	}
	if req.Messages[0].Injected {
		t.Error("user's own message must not be marked Injected")
	}
	if c := req.Messages[2]; c.Role != "user" || c.Content != autoContinuePrompt || !c.Injected {
		t.Errorf("continue turn = %+v, want injected user %q", c, autoContinuePrompt)
	}
}
