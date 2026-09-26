package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// conduit-31jg.18(a): when the quota-fallback call timed out, the bd-13p
// timeout retry went to the ORIGINAL provider carrying the FALLBACK model
// name (req.Model had already been switched). (provider, model) must travel
// as a pair.

var errFallbackTimeout = errors.New(`post https://api.z.ai/api/paas/v4/chat/completions: context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)

func assertNoFallbackModelOnPrimary(t *testing.T, primary *MockProvider, fallbackModel string) {
	t.Helper()
	for i, c := range primary.GetCalls() {
		if c.Request.Model == fallbackModel {
			t.Errorf("primary call %d carried the fallback model %q (conduit-31jg.18)", i, fallbackModel)
		}
	}
}

func TestFallbackTimeoutRetryStaysOnFallbackRoute(t *testing.T) {
	const fbModel = "fallbackprov/fb-model-x"
	paths := map[string]func(r *Router) (string, error){
		"generate": func(r *Router) (string, error) {
			resp, err := r.GenerateResponse(context.Background(), newFallbackSession(t), "hi", "primary")
			if err != nil {
				return "", err
			}
			return resp.Content, nil
		},
		"tools": func(r *Router) (string, error) {
			resp, err := r.GenerateResponseWithToolsAndProgress(context.Background(), newFallbackSession(t), "hi", "primary", "claude-haiku-4-5-20251001", nil)
			if err != nil {
				return "", err
			}
			return resp.GetContent(), nil
		},
		"streaming": func(r *Router) (string, error) {
			resp, err := r.GenerateResponseStreaming(context.Background(), newFallbackSession(t), "hi", "primary", "claude-haiku-4-5-20251001", func(string, bool) {})
			if err != nil {
				return "", err
			}
			return resp.GetContent(), nil
		},
	}
	for name, run := range paths {
		t.Run(name, func(t *testing.T) {
			router, primary, fallback := newFallbackTestRouter(t)
			primary.SetResponses([]MockResponse{
				{Error: fmt.Errorf("API error: 400 - out of extra usage")},
				{Content: "WRONG: primary served the retry"},
			})
			fallback.SetResponses([]MockResponse{
				{Error: errFallbackTimeout},
				{Content: "fallback recovered"},
			})

			content, err := run(router)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if content != "fallback recovered" {
				t.Errorf("content = %q, want %q", content, "fallback recovered")
			}
			if n := primary.GetCallCount(); n != 1 {
				t.Errorf("primary called %d times, want 1 (timeout retry must not go back to it)", n)
			}
			if n := fallback.GetCallCount(); n != 2 {
				t.Errorf("fallback called %d times, want 2 (fallback + its timeout retry)", n)
			}
			for i, c := range fallback.GetCalls() {
				if c.Request.Model != fbModel {
					t.Errorf("fallback call %d model = %q, want %q", i, c.Request.Model, fbModel)
				}
			}
			assertNoFallbackModelOnPrimary(t, primary, fbModel)
		})
	}
}

func TestFallbackTimeoutRetryFailure_NeverHitsPrimaryWithFallbackModel(t *testing.T) {
	router, primary, fallback := newFallbackTestRouter(t)
	primary.SetResponses([]MockResponse{{Error: fmt.Errorf("API error: 400 - out of extra usage")}})
	fallback.SetResponses([]MockResponse{{Error: errFallbackTimeout}, {Error: errFallbackTimeout}})

	_, err := router.GenerateResponseWithToolsAndProgress(context.Background(), newFallbackSession(t), "hi", "primary", "claude-haiku-4-5-20251001", nil)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected the fallback's timeout error, got %v", err)
	}
	if n := primary.GetCallCount(); n != 1 {
		t.Errorf("primary called %d times, want 1", n)
	}
	assertNoFallbackModelOnPrimary(t, primary, "fallbackprov/fb-model-x")
}

// A retry after text was already streamed must not re-send that text.
func TestStreamingTimeoutRetryDoesNotDuplicateStreamedText(t *testing.T) {
	router, primary, _ := newFallbackTestRouter(t)
	sp := &partialThenErrorStreamer{MockProvider: primary, partial: "Hello, wor", err: errFallbackTimeout, final: "Hello, world."}
	router.RegisterProvider("primary", sp)

	var streamed strings.Builder
	resp, err := router.GenerateResponseStreaming(context.Background(), newFallbackSession(t), "hi", "primary", "claude-haiku-4-5-20251001",
		func(delta string, done bool) { streamed.WriteString(delta) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetContent() != "Hello, world." {
		t.Errorf("final content = %q, want the retried generation", resp.GetContent())
	}
	if got := streamed.String(); got != "Hello, wor" {
		t.Errorf("client received %q; the retry must be muted once text was streamed", got)
	}
	if sp.calls != 2 {
		t.Errorf("streaming calls = %d, want 2", sp.calls)
	}
}

// partialThenErrorStreamer streams `partial` then fails with err on the
// first call; later calls stream `final` in full and succeed.
type partialThenErrorStreamer struct {
	*MockProvider
	partial, final string
	err            error
	calls          int
}

func (p *partialThenErrorStreamer) GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error) {
	p.calls++
	if p.calls == 1 {
		onDelta(p.partial, false)
		return &GenerateResponse{Content: p.partial, Partial: true}, p.err
	}
	onDelta(p.final, false)
	onDelta("", true)
	return &GenerateResponse{Content: p.final}, nil
}
