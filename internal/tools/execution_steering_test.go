package tools

import (
	"context"
	"testing"

	"conduit/internal/ai"
)

// conduit-5uw3: a steering message is appended after the round's tool
// results as a real (non-Injected) user message and stays in history for
// later rounds; the source is drained once per round boundary.
func TestHandleToolCallFlow_FoldsSteeringMessages(t *testing.T) {
	reg := NewMockRegistry()
	reg.AddTool(&MockTool{name: "Probe"})
	engine := NewExecutionEngine(reg, 1, 0, 10)

	provider := ai.NewMockProvider("mock")
	provider.AddResponse("", []ai.ToolCall{{ID: "c2", Name: "Probe", Args: map[string]interface{}{}}})
	provider.AddResponse("done", nil)

	drains := 0
	ctx := WithSteeringSource(context.Background(), func(context.Context) []string {
		drains++
		if drains == 1 {
			return []string{"use prod-2"}
		}
		return nil
	})
	_, err := engine.HandleToolCallFlow(ctx, provider,
		&ai.GenerateRequest{Messages: []ai.ChatMessage{{Role: "user", Content: "deploy"}}},
		&ai.GenerateResponse{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "Probe", Args: map[string]interface{}{}}}},
	)
	if err != nil {
		t.Fatalf("HandleToolCallFlow: %v", err)
	}
	if drains != 2 {
		t.Fatalf("source drained %d times, want once per round boundary (2)", drains)
	}
	calls := provider.GetCalls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}
	first := calls[0].Request.Messages
	if last := first[len(first)-1]; last.Role != "user" || last.Content != "use prod-2" || last.Injected {
		t.Fatalf("round 1 must end with the steering message as a real user message, got %+v", last)
	}
	if first[len(first)-2].Role != "tool" {
		t.Fatalf("steering message must follow the tool results: %+v", first)
	}
	n := 0
	for _, m := range calls[1].Request.Messages {
		if m.Role == "user" && m.Content == "use prod-2" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("steering message appears %d times in round 2, want exactly once", n)
	}
}

// A nil source (non-steerable turn) detaches an inherited one.
func TestWithSteeringSource_NilDetaches(t *testing.T) {
	outer := WithSteeringSource(context.Background(), func(context.Context) []string { return []string{"x"} })
	if got := takeSteering(WithSteeringSource(outer, nil)); got != nil {
		t.Fatalf("nested nil source drained %v", got)
	}
}
