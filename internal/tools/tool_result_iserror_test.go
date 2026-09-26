package tools

import (
	"context"
	"testing"

	"conduit/internal/ai"
)

// conduit-31jg.45: tool messages appended by HandleToolCallFlow carry
// IsError for Go errors and soft (Success=false) failures only.
func TestHandleToolCallFlow_SetsIsErrorOnFailedResults(t *testing.T) {
	engine := newChainTestEngine(t)
	p := ai.NewMockProvider("m")
	p.AddResponse("done", nil)

	initial := &ai.GenerateResponse{ToolCalls: []ai.ToolCall{
		{ID: "h", Name: "hard_fail", Args: map[string]interface{}{}},
		{ID: "s", Name: "soft_fail", Args: map[string]interface{}{}},
		{ID: "o", Name: "ok_a", Args: map[string]interface{}{}},
	}}
	if _, err := engine.HandleToolCallFlow(context.Background(), p, chainReq("go"), initial); err != nil {
		t.Fatal(err)
	}
	calls := p.GetCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 provider call, got %d", len(calls))
	}
	want := map[string]bool{"h": true, "s": true, "o": false}
	seen := 0
	for _, m := range calls[0].Request.Messages {
		if m.Role != "tool" {
			continue
		}
		seen++
		if m.IsError != want[m.ToolCallID] {
			t.Errorf("tool %s IsError=%v, want %v", m.ToolCallID, m.IsError, want[m.ToolCallID])
		}
	}
	if seen != 3 {
		t.Fatalf("want 3 tool messages, got %d", seen)
	}
}
