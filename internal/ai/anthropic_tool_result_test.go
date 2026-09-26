package ai

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/config"
)

// conduit-31jg.45: failed tool results carry is_error, and a round's
// tool_results (plus trailing guidance text) arrive in ONE user message.

func toolRoundRequest() *GenerateRequest {
	return &GenerateRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		Messages: []ChatMessage{
			{Role: "user", Content: "do two things"},
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "t1", Name: "Bash", Args: map[string]interface{}{"command": "false"}},
				{ID: "t2", Name: "Read", Args: map[string]interface{}{"path": "x"}},
			}},
			{Role: "tool", ToolCallID: "t1", Content: "Tool 'Bash' failed: exit 1", IsError: true},
			{Role: "tool", ToolCallID: "t2", Content: "file contents"},
			{Role: "user", Content: "[Tool-loop guidance] try something else"},
		},
	}
}

func sendAndCapture(t *testing.T, cfg config.PromptCachingConfig, req *GenerateRequest) map[string]interface{} {
	t.Helper()
	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, cfg))
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.GenerateResponse(context.Background(), req); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return capturedRequest(t, raw)
}

func TestAnthropicToolResult_IsErrorAndSingleUserMessage(t *testing.T) {
	body := sendAndCapture(t, config.PromptCachingConfig{Enabled: false}, toolRoundRequest())
	msgs := body["messages"].([]interface{})
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages (user, assistant, user[tool_results+guidance]), got %d: %v", len(msgs), msgs)
	}
	last := msgs[2].(map[string]interface{})
	if last["role"] != "user" {
		t.Fatalf("last role = %v", last["role"])
	}
	blocks := last["content"].([]interface{})
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks (2 tool_result + text), got %d: %v", len(blocks), blocks)
	}
	b0 := blocks[0].(map[string]interface{})
	b1 := blocks[1].(map[string]interface{})
	b2 := blocks[2].(map[string]interface{})
	if b0["type"] != "tool_result" || b0["tool_use_id"] != "t1" || b0["is_error"] != true {
		t.Errorf("failed result must carry is_error=true: %v", b0)
	}
	if b1["type"] != "tool_result" || b1["tool_use_id"] != "t2" {
		t.Errorf("second tool_result wrong: %v", b1)
	}
	if _, ok := b1["is_error"]; ok {
		t.Errorf("successful result must not carry is_error: %v", b1)
	}
	if b2["type"] != "text" || !strings.Contains(b2["text"].(string), "Tool-loop guidance") {
		t.Errorf("guidance text must trail the tool results: %v", b2)
	}
}

// A plain user message that does not follow tool results is not merged.
func TestAnthropicToolResult_PlainUserTurnsUntouched(t *testing.T) {
	req := &GenerateRequest{
		Model: "claude-sonnet-4-6", MaxTokens: 100,
		Messages: []ChatMessage{
			{Role: "user", Content: "a"},
			{Role: "assistant", Content: "b"},
			{Role: "user", Content: "c"},
		},
	}
	body := sendAndCapture(t, config.PromptCachingConfig{Enabled: false}, req)
	msgs := body["messages"].([]interface{})
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	if msgs[2].(map[string]interface{})["content"] != "c" {
		t.Errorf("plain user content changed: %v", msgs[2])
	}
}
