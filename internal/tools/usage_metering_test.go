package tools

import (
	"context"
	"sync"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
)

// conduit-31jg.64 end-to-end: the real ExecutionEngine behind the real
// Router. A 3-call tool chain plus one EmptyGuard retry is 4 provider calls,
// and the usage tracker (fuel gauge feed) sees exactly 4.

type callCounter struct {
	mu    sync.Mutex
	calls int
	in    int
}

func (c *callCounter) OnUsage(_, _ string, in, _, _, _ int, _ int64) {
	c.mu.Lock()
	c.calls++
	c.in += in
	c.mu.Unlock()
}
func (c *callCounter) OnError(string, string) {}

func TestRouterWithEngine_EveryProviderCallMetered(t *testing.T) {
	engine := newChainTestEngine(t)
	r, err := ai.NewRouterWithExecution(config.AIConfig{DefaultProvider: "m"}, nil, NewExecutionEngineAdapter(engine))
	if err != nil {
		t.Fatal(err)
	}
	p := ai.NewMockProvider("m")
	r.RegisterProvider("m", p)
	obs := &callCounter{}
	r.GetUsageTracker().SetObserver(obs)

	tc := []ai.ToolCall{{ID: "a", Name: "ok_a", Args: map[string]interface{}{}}}
	p.SetResponses([]ai.MockResponse{
		{Content: "", Usage: ai.Usage{PromptTokens: 1}},                              // empty → guard retry
		{ToolCalls: tc, Usage: ai.Usage{PromptTokens: 10}},                           // chain call 1
		{ToolCalls: tc, Usage: ai.Usage{PromptTokens: 100}},                          // chain call 2
		{Content: "done", FinishReason: "stop", Usage: ai.Usage{PromptTokens: 1000}}, // chain call 3
	})
	resp, err := r.GenerateResponseWithTools(context.Background(), &sessions.Session{Key: "k"}, "go", "", "claude-sonnet-4-6")
	if err != nil {
		t.Fatal(err)
	}
	if p.GetCallCount() != 4 {
		t.Fatalf("provider calls = %d, want 4", p.GetCallCount())
	}
	if obs.calls != 4 || obs.in != 1111 {
		t.Fatalf("metered calls=%d prompt=%d, want 4/1111", obs.calls, obs.in)
	}
	if u := resp.GetUsage(); u.PromptTokens != 1111 || u.PricedCalls != 4 {
		t.Fatalf("turn usage = %+v, want prompt 1111 over 4 priced calls", *u)
	}
}
