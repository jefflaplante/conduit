package tools

import (
	"context"
	"testing"
	"time"

	"conduit/internal/ai"
)

// conduit-31jg.15: the ConversationResponse usage is the WHOLE turn — every
// depth, EmptyGuard retry and auto-continue — including cache token fields.
// combineUsage used to sum only the initial and the last round trip and drop
// the cache fields.
func TestHandleToolCallFlow_UsageCoversWholeTurn(t *testing.T) {
	engine := newChainTestEngine(t)
	p := ai.NewMockProvider("m")
	tc := func(id string) []ai.ToolCall {
		return []ai.ToolCall{{ID: id, Name: "ok_a", Args: map[string]interface{}{}}}
	}
	u := func(p, c, cw, cr int) ai.Usage {
		return ai.Usage{PromptTokens: p, CompletionTokens: c, TotalTokens: p + c,
			CacheCreationInputTokens: cw, CacheReadInputTokens: cr}
	}
	p.SetResponses([]ai.MockResponse{
		{ToolCalls: tc("d0"), Usage: u(10, 20, 50, 1000)},                        // depth 0 → tools
		{Content: "", Usage: u(5, 0, 0, 1100)},                                   // depth 1: raw empty
		{ToolCalls: tc("d1"), Usage: u(10, 20, 60, 1100)},                        // depth 1: EmptyGuard retry
		{Content: "part1 ", FinishReason: "length", Usage: u(10, 4000, 0, 1200)}, // depth 2: truncated
		{Content: "part2", FinishReason: "stop", Usage: u(4000, 30, 0, 1200)},    // depth 2: auto-continue
	})
	initial := &ai.GenerateResponse{ToolCalls: tc("i"), Usage: u(100, 10, 1000, 0)}

	resp, err := engine.HandleToolCallFlow(context.Background(), p, chainReq("go"), initial)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "part1 part2" {
		t.Fatalf("content = %q", resp.Content)
	}
	if got := p.GetCallCount(); got != 5 {
		t.Fatalf("provider calls = %d, want 5", got)
	}
	want := ai.Usage{
		PromptTokens:             100 + 10 + 5 + 10 + 10 + 4000,
		CompletionTokens:         10 + 20 + 0 + 20 + 4000 + 30,
		CacheCreationInputTokens: 1000 + 50 + 0 + 60 + 0 + 0,
		CacheReadInputTokens:     0 + 1000 + 1100 + 1100 + 1200 + 1200,
	}
	want.TotalTokens = want.PromptTokens + want.CompletionTokens
	got := resp.Usage
	if got == nil {
		t.Fatal("nil usage")
	}
	if got.PromptTokens != want.PromptTokens || got.CompletionTokens != want.CompletionTokens ||
		got.TotalTokens != want.TotalTokens || got.CacheCreationInputTokens != want.CacheCreationInputTokens ||
		got.CacheReadInputTokens != want.CacheReadInputTokens {
		t.Errorf("usage = %+v\nwant    %+v", *got, want)
	}
	// Context gauge = the last round trip's full prompt, not the turn sum.
	if c := got.Context(); c != 4000+1200 {
		t.Errorf("Context() = %d, want %d", c, 4000+1200)
	}
}

// Early stops (depth limit) also report the accumulated turn usage.
func TestHandleToolCallFlow_UsageOnDepthLimit(t *testing.T) {
	registry := NewMockRegistry()
	registry.AddTool(&MockTool{name: "ok_a", parameters: map[string]interface{}{"type": "object"},
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Success: true, Content: "a"}, nil
		}})
	engine := NewExecutionEngine(registry, 1, 30*time.Second, 2)
	p := ai.NewMockProvider("m")
	call := []ai.ToolCall{{ID: "x", Name: "ok_a", Args: map[string]interface{}{}}}
	p.SetResponses([]ai.MockResponse{
		{ToolCalls: call, Usage: ai.Usage{PromptTokens: 7, CompletionTokens: 3, CacheReadInputTokens: 11}},
		{ToolCalls: call, Usage: ai.Usage{PromptTokens: 7, CompletionTokens: 3, CacheReadInputTokens: 11}},
	})
	initial := &ai.GenerateResponse{ToolCalls: call, Usage: ai.Usage{PromptTokens: 1, CompletionTokens: 1}}
	resp, err := engine.HandleToolCallFlow(context.Background(), p, chainReq("go"), initial)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 15 || resp.Usage.CacheReadInputTokens != 22 {
		t.Errorf("depth-limit usage = %+v, want prompt 15 cache_read 22", resp.Usage)
	}
}
