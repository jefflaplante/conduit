package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// conduit-31jg.11: Anthropic stop_reason was never mapped to FinishReason, so
// the bd-1k3o max_tokens guard was dead for Anthropic, truncated replies
// looked complete, and a tool_use severed by max_tokens was executed with
// partial/empty input.

// anthropicJSONServer replies to each request with the next body in bodies
// (the last one repeats) and records the request bodies it received.
func anthropicJSONServer(t *testing.T, bodies ...map[string]interface{}) (*httptest.Server, func() []map[string]interface{}) {
	t.Helper()
	var mu sync.Mutex
	var received []map[string]interface{}
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var got map[string]interface{}
		_ = json.Unmarshal(raw, &got)
		mu.Lock()
		received = append(received, got)
		body := bodies[len(bodies)-1]
		if n < len(bodies) {
			body = bodies[n]
		}
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]interface{} {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]interface{}(nil), received...)
	}
}

func anthropicMsg(stopReason string, blocks ...map[string]interface{}) map[string]interface{} {
	content := make([]interface{}, 0, len(blocks))
	for _, b := range blocks {
		content = append(content, b)
	}
	return map[string]interface{}{
		"id":          "msg_test",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-sonnet-4-6",
		"content":     content,
		"stop_reason": stopReason,
		"usage":       map[string]interface{}{"input_tokens": 10, "output_tokens": 5},
	}
}

func textBlock(s string) map[string]interface{} {
	return map[string]interface{}{"type": "text", "text": s}
}

func toolUseBlock(id, name string, input map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": input}
}

func newTestAnthropic(t *testing.T, url string) *AnthropicProvider {
	t.Helper()
	p, err := NewAnthropicProvider(config.ProviderConfig{
		Name:    "anthropic",
		APIKey:  "sk-ant-api03-test",
		BaseURL: url,
		Model:   "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	return p
}

func TestAnthropicStopReasonMapping(t *testing.T) {
	cases := map[string]string{
		"end_turn":      "stop",
		"stop_sequence": "stop",
		"max_tokens":    "length",
		"tool_use":      "tool_calls",
		"refusal":       "content_filter",
		"pause_turn":    "stop",
		"":              "",
	}
	for in, want := range cases {
		if got := mapAnthropicStopReason(in); got != want {
			t.Errorf("mapAnthropicStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnthropicGenerateResponse_MaxTokensSetsFinishReason(t *testing.T) {
	srv, _ := anthropicJSONServer(t, anthropicMsg("max_tokens", textBlock("cut off mid-sen")))
	p := newTestAnthropic(t, srv.URL)

	resp, err := p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if resp.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want length", resp.FinishReason)
	}
	if resp.StopReason != "max_tokens" {
		t.Errorf("StopReason = %q, want max_tokens", resp.StopReason)
	}
}

func TestAnthropicGenerateResponse_TruncatedToolUseDropped(t *testing.T) {
	srv, _ := anthropicJSONServer(t, anthropicMsg("max_tokens",
		textBlock("Let me write that."),
		toolUseBlock("toolu_ok", "Read", map[string]interface{}{"path": "/a"}),
		toolUseBlock("toolu_cut", "Write", map[string]interface{}{}), // severed mid-input
	))
	p := newTestAnthropic(t, srv.URL)

	resp, err := p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_ok" {
		t.Fatalf("expected only the complete tool call toolu_ok, got %+v", resp.ToolCalls)
	}
	if resp.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want length", resp.FinishReason)
	}
}

func TestAnthropicGenerateResponse_ToolUseStopKeepsCalls(t *testing.T) {
	srv, _ := anthropicJSONServer(t, anthropicMsg("tool_use",
		toolUseBlock("toolu_1", "Read", map[string]interface{}{"path": "/a"}),
	))
	p := newTestAnthropic(t, srv.URL)
	resp, err := p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}}, MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if resp.FinishReason != "tool_calls" || len(resp.ToolCalls) != 1 {
		t.Fatalf("want tool_calls with 1 call, got %q / %d", resp.FinishReason, len(resp.ToolCalls))
	}
}

func TestAnthropicGenerateResponse_RefusalIsFinal(t *testing.T) {
	srv, _ := anthropicJSONServer(t, anthropicMsg("refusal",
		toolUseBlock("toolu_1", "Bash", map[string]interface{}{"command": "x"}),
	))
	p := newTestAnthropic(t, srv.URL)
	resp, err := p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}}, MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if resp.FinishReason != "content_filter" {
		t.Errorf("FinishReason = %q, want content_filter", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("refusal must not carry tool calls, got %+v", resp.ToolCalls)
	}
	if resp.Content != refusalFallbackContent {
		t.Errorf("empty refusal should get visible content, got %q", resp.Content)
	}
	if IsEmptyModelResponse(resp) {
		t.Error("refusal must not look like a raw-empty response (would be retried)")
	}
}

func TestParseSSEStream_TruncatedToolUseDropped(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Writing now."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_cut","name":"Write","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \"/tmp/x\", \"content\": \"abc"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":10}}

event: message_stop
data: {"type":"message_stop"}

`
	p := &AnthropicProvider{model: "test-model"}
	resp, err := p.parseSSEStream(strings.NewReader(sse), nil)
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("truncated tool_use must be dropped, got %+v", resp.ToolCalls)
	}
	if resp.FinishReason != "length" || resp.StopReason != "max_tokens" {
		t.Errorf("want length/max_tokens, got %q/%q", resp.FinishReason, resp.StopReason)
	}
	if resp.Content != "Writing now." {
		t.Errorf("content = %q", resp.Content)
	}
}

// recordingExecEngine records whether the tool loop was entered.
type recordingExecEngine struct {
	mu    sync.Mutex
	calls int
}

func (e *recordingExecEngine) HandleToolCallFlow(ctx context.Context, provider Provider, initialReq *GenerateRequest, initialResp *GenerateResponse) (ConversationResponse, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return &SimpleConversationResponse{Content: "executed tools"}, nil
}

// End-to-end through the router: a first reply that is text + a tool_use
// severed by max_tokens must NOT reach the tool loop; the router's first
// round trip auto-continues instead.
func TestRouter_TruncatedToolUseNotExecuted_AutoContinues(t *testing.T) {
	srv, received := anthropicJSONServer(t,
		anthropicMsg("max_tokens",
			textBlock("Part one, "),
			toolUseBlock("toolu_cut", "Write", map[string]interface{}{}),
		),
		anthropicMsg("end_turn", textBlock("part two.")),
	)
	p := newTestAnthropic(t, srv.URL)

	exec := &recordingExecEngine{}
	r := &Router{
		providers:       map[string]Provider{"anthropic": p},
		providerMeta:    map[string]ProviderMeta{"anthropic": {Name: "anthropic", Type: "anthropic"}},
		default_:        "anthropic",
		executionEngine: exec,
	}

	resp, err := r.GenerateResponseWithTools(context.Background(), &sessions.Session{}, "do it", "anthropic", "")
	if err != nil {
		t.Fatalf("GenerateResponseWithTools: %v", err)
	}
	if exec.calls != 0 {
		t.Fatalf("truncated tool_use reached the tool loop (%d calls)", exec.calls)
	}
	if got := resp.GetContent(); got != "Part one, part two." {
		t.Errorf("content = %q, want concatenated fragments", got)
	}

	reqs := received()
	if len(reqs) != 2 {
		t.Fatalf("expected initial + 1 continuation request, got %d", len(reqs))
	}
	msgs, _ := reqs[1]["messages"].([]interface{})
	if len(msgs) < 2 {
		t.Fatalf("continuation request has %d messages", len(msgs))
	}
	last, _ := msgs[len(msgs)-1].(map[string]interface{})
	prev, _ := msgs[len(msgs)-2].(map[string]interface{})
	if last["role"] != "user" || prev["role"] != "assistant" {
		t.Errorf("continuation should end with assistant fragment + user continue, got %v / %v", prev["role"], last["role"])
	}
	if s, _ := prev["content"].(string); s != "Part one, " {
		t.Errorf("assistant fragment = %v, want text only (no severed tool_use)", prev["content"])
	}
}

func TestContinueLengthTruncated_NoopOnStop(t *testing.T) {
	m := NewMockProvider("m")
	in := &GenerateResponse{Content: "done", FinishReason: "stop"}
	out := ContinueLengthTruncated(context.Background(), m, &GenerateRequest{}, in, "t")
	if out != in || len(m.calls) != 0 {
		t.Fatalf("expected passthrough without provider calls")
	}
}

func TestContinueLengthTruncated_MarkerAfterBudget(t *testing.T) {
	m := NewMockProvider("m")
	m.AddResponseWithFinishReason("b", nil, "length")
	m.AddResponseWithFinishReason("c", nil, "length")
	req := &GenerateRequest{Messages: []ChatMessage{{Role: "user", Content: "q"}}}
	out := ContinueLengthTruncated(context.Background(), m, req, &GenerateResponse{Content: "a", FinishReason: "length"}, "t")
	if len(m.calls) != maxLengthAutoContinues {
		t.Fatalf("expected %d continuations, got %d", maxLengthAutoContinues, len(m.calls))
	}
	if out.Content != "abc"+lengthTruncatedMarker {
		t.Errorf("content = %q", out.Content)
	}
}
