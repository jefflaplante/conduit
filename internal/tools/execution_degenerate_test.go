package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
)

// bd-1k3o: degenerate finals in the tool loop must not silently complete a
// sub-agent turn. Two mechanisms, both proven in the 2026-09-04 RCA
// (memory/sd-bd-f48o-followup.md):
//
//  1. length-truncation: finish_reason=="length" + 0 tool calls — the answer
//     was cut off at max_tokens mid-sentence. Fix: auto-continue (send
//     "continue" as the next user turn, up to 2 times), so the model finishes
//     the report instead of delivering a severed fragment.
//  2. empty-guard fallback: GuardEmptyResponse substituted its terminal
//     fallback text after raw-empty responses — that content is locally
//     generated, not a model answer. Fix: surface it as an ERROR from the
//     tool flow so subagents.go routes to WakeSourceSubAgentFailed.

// --- helper ---

func newDegenerateEngine(t *testing.T) (*ExecutionEngine, *MockRegistry) {
	t.Helper()
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "test_tool",
		description: "A test tool",
		parameters:  map[string]interface{}{"type": "object"},
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Success: true, Content: "tool result"}, nil
		},
	}
	registry.AddTool(tool)
	engine := NewExecutionEngine(registry, 3, 30*time.Second, 10)
	return engine, registry
}

func degenerateInitialReqResp() (*ai.GenerateRequest, *ai.GenerateResponse) {
	req := &ai.GenerateRequest{
		Messages:  []ai.ChatMessage{{Role: "user", Content: "write the report"}},
		Model:     "test-model",
		Tools:     []ai.Tool{{Name: "test_tool"}},
		MaxTokens: 1024,
	}
	resp := &ai.GenerateResponse{
		ToolCalls: []ai.ToolCall{
			{ID: "call_1", Name: "test_tool", Args: map[string]interface{}{}},
		},
	}
	return req, resp
}

// --- 1. length-truncation auto-continue ---

func TestHandleToolCallFlow_LengthTruncatedFinal_AutoContinues(t *testing.T) {
	engine, _ := newDegenerateEngine(t)
	provider := ai.NewMockProvider("test")
	// First final: truncated at max_tokens (finish_reason=length), no tool calls
	provider.AddResponseWithFinishReason("part one of the report, cut off mid sente", nil, "length")
	// Auto-continue produces the rest of the answer
	provider.AddResponse("nce. Part two: the conclusion.", nil)

	req, resp := degenerateInitialReqResp()
	result, err := engine.HandleToolCallFlow(context.Background(), provider, req, resp)
	if err != nil {
		t.Fatalf("HandleToolCallFlow failed: %v", err)
	}

	want := "part one of the report, cut off mid sentence. Part two: the conclusion."
	if result.Content != want {
		t.Errorf("expected continued content %q, got %q", want, result.Content)
	}

	// Two follow-up LLM calls: truncated final + continuation
	if got := provider.GetCallCount(); got != 2 {
		t.Errorf("expected 2 provider calls, got %d", got)
	}

	// The continuation request must carry a user turn prompting completion
	calls := provider.GetCalls()
	if len(calls) != 2 || len(calls[1].Request.Messages) == 0 {
		t.Fatalf("expected 2 calls with messages, got %+v", calls)
	}
	lastMsg := calls[1].Request.Messages[len(calls[1].Request.Messages)-1]
	if lastMsg.Role != "user" {
		t.Errorf("expected trailing user message for continue, got role=%q content=%q", lastMsg.Role, lastMsg.Content)
	}
}

func TestHandleToolCallFlow_LengthTruncatedFinal_CapAtTwoContinues(t *testing.T) {
	engine, _ := newDegenerateEngine(t)
	provider := ai.NewMockProvider("test")
	// All finals truncated — auto-continue must stop after 2 continues and
	// still return the (concatenated) content rather than looping forever.
	provider.AddResponseWithFinishReason("a", nil, "length")
	provider.AddResponseWithFinishReason("b", nil, "length")
	provider.AddResponseWithFinishReason("c", nil, "length")

	req, resp := degenerateInitialReqResp()
	result, err := engine.HandleToolCallFlow(context.Background(), provider, req, resp)
	if err != nil {
		t.Fatalf("HandleToolCallFlow failed: %v", err)
	}

	if got := provider.GetCallCount(); got != 3 {
		t.Errorf("expected 3 provider calls (final + 2 continues), got %d", got)
	}
	content := result.Content
	if content == "" {
		t.Error("expected non-empty content even when all parts truncated")
	}
	if !containsAll(content, "a", "b", "c") {
		t.Errorf("expected concatenated parts a+b+c, got %q", content)
	}
}

// --- 2. empty-guard fallback marker ---
//
// The routing decision lives in subagents.go (see subagents_degenerate_test.go):
// erroring from HandleToolCallFlow would regress the conduit-18vj guarantee
// that main-session users see the friendly fallback text instead of an error.
// Here we verify the exported marker itself.

func TestIsEmptyResponseFallback(t *testing.T) {
	fallback := ai.EmptyResponseFallbackContent()
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"exact", fallback, true},
		{"whitespace padded", "  " + fallback + "\n", true},
		{"similar but different", fallback + " extra words", false},
		{"normal content", "Task completed successfully.", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ai.IsEmptyResponseFallback(tc.content); got != tc.want {
				t.Errorf("IsEmptyResponseFallback(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
