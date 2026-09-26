package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// conduit-31jg.14: the system cache breakpoint must sit on the byte-stable
// static block, never on the per-turn dynamic block; the last message gets
// a rolling breakpoint that counts tool content; never more than 4 markers.

var allCaching = config.PromptCachingConfig{
	Enabled: true, CacheTools: true, CacheSystem: true, CacheHistory: true,
	HistoryBreakpointInterval: 6,
}

// splitSystemMessage builds the system message exactly as the router does.
func splitSystemMessage(t *testing.T, static, dynamic string) ChatMessage {
	t.Helper()
	r := &Router{}
	msgs, err := r.buildChatMessagesWithSystemPrompt(context.Background(), &sessions.Session{Key: "k"}, "hi",
		[]SystemBlock{{Type: "text", Text: static}, {Type: "text", Text: dynamic, Dynamic: true}})
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Role != "system" {
		t.Fatalf("first message role = %q", msgs[0].Role)
	}
	return msgs[0]
}

func systemBlocksOf(t *testing.T, body map[string]interface{}) []map[string]interface{} {
	t.Helper()
	arr, ok := body["system"].([]interface{})
	if !ok {
		t.Fatalf("system is %T, want block array", body["system"])
	}
	out := make([]map[string]interface{}, len(arr))
	for i, b := range arr {
		out[i] = b.(map[string]interface{})
	}
	return out
}

// countCacheControl counts cache_control keys anywhere in the request body.
func countCacheControl(v interface{}) int {
	n := 0
	switch x := v.(type) {
	case map[string]interface{}:
		for k, vv := range x {
			if k == "cache_control" {
				n++
			}
			n += countCacheControl(vv)
		}
	case []interface{}:
		for _, vv := range x {
			n += countCacheControl(vv)
		}
	}
	return n
}

func TestCacheSplit_StaticBlockCarriesBreakpoint(t *testing.T) {
	static := strings.Repeat("static instructions ", 1000) // ~5000 tokens
	dynamic := "## Time Context\nCurrent time: Sat 2026-09-26 21:14 PDT"
	req := &GenerateRequest{Model: "claude-sonnet-4-6", MaxTokens: 100, Messages: []ChatMessage{
		splitSystemMessage(t, static, dynamic),
		{Role: "user", Content: "hi"},
	}}
	body := sendAndCapture(t, allCaching, req)
	blocks := systemBlocksOf(t, body)
	if len(blocks) != 2 {
		t.Fatalf("want 2 system blocks, got %d", len(blocks))
	}
	if blocks[0]["text"] != static {
		t.Errorf("block 0 is not the static text")
	}
	if _, ok := blocks[0]["cache_control"]; !ok {
		t.Errorf("static block must carry cache_control: %v", blocks[0]["cache_control"])
	}
	if blocks[1]["text"] != dynamic {
		t.Errorf("block 1 = %q, want dynamic text", blocks[1]["text"])
	}
	if _, ok := blocks[1]["cache_control"]; ok {
		t.Errorf("dynamic block must NOT carry cache_control")
	}
	if _, ok := blocks[1]["dynamic"]; ok {
		t.Errorf("internal Dynamic flag leaked into the API request")
	}
}

// OAuth prepends the identity block; the breakpoint still lands on the
// static agent block, not the identity and not the dynamic one.
func TestCacheSplit_OAuthIdentityThenStatic(t *testing.T) {
	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, allCaching))
	if err != nil {
		t.Fatal(err)
	}
	p.isOAuth, p.authCfg = true, nil
	static := strings.Repeat("s ", 10000)
	req := &GenerateRequest{Model: "claude-sonnet-4-6", MaxTokens: 100, Messages: []ChatMessage{
		splitSystemMessage(t, static, "now"),
		{Role: "user", Content: "hi"},
	}}
	if _, err := p.GenerateResponse(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	blocks := systemBlocksOf(t, capturedRequest(t, raw))
	if len(blocks) != 3 {
		t.Fatalf("want identity+static+dynamic, got %d", len(blocks))
	}
	for i, want := range []bool{false, true, false} {
		if _, got := blocks[i]["cache_control"]; got != want {
			t.Errorf("block %d cache_control=%v, want %v", i, got, want)
		}
	}
}

// If something rewrote the joined Content after the prompt was built, the
// block split is stale: send Content as one static block.
func TestCacheSplit_StaleBlocksFallBackToContent(t *testing.T) {
	msg := splitSystemMessage(t, strings.Repeat("a ", 5000), "b")
	msg.Content += "\n\nappended later"
	req := &GenerateRequest{Model: "claude-sonnet-4-6", MaxTokens: 100, Messages: []ChatMessage{msg, {Role: "user", Content: "hi"}}}
	blocks := systemBlocksOf(t, sendAndCapture(t, allCaching, req))
	if len(blocks) != 1 || blocks[0]["text"] != msg.Content {
		t.Fatalf("want single block with full Content, got %d blocks", len(blocks))
	}
}

// Without cache markers, API-key auth still sends the joined string —
// identical to the pre-split prompt text.
func TestCacheSplit_APIKeyNoCachingSendsJoinedString(t *testing.T) {
	msg := splitSystemMessage(t, "static", "dynamic")
	req := &GenerateRequest{Model: "claude-sonnet-4-6", MaxTokens: 100, Messages: []ChatMessage{msg, {Role: "user", Content: "hi"}}}
	body := sendAndCapture(t, config.PromptCachingConfig{Enabled: false}, req)
	if body["system"] != "static\n\ndynamic" {
		t.Fatalf("system = %#v, want joined string", body["system"])
	}
}

// toolLoopRequest is a mid-chain request: a tiny system prompt and a history
// made only of tool turns (block content), ending in tool results plus the
// ephemeral loop guidance.
func toolLoopRequest(rounds int, resultSize int) *GenerateRequest {
	req := &GenerateRequest{Model: "claude-sonnet-4-6", MaxTokens: 100}
	req.Messages = append(req.Messages, ChatMessage{Role: "system", Content: "short"}, ChatMessage{Role: "user", Content: "go"})
	for i := 0; i < rounds; i++ {
		id := "t" + string(rune('a'+i))
		req.Messages = append(req.Messages,
			ChatMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "Bash", Args: map[string]interface{}{"command": "ls"}}}},
			ChatMessage{Role: "tool", ToolCallID: id, Content: strings.Repeat("output ", resultSize)},
		)
	}
	req.Messages = append(req.Messages, ChatMessage{Role: "user", Content: "[Tool-loop guidance] try another way"})
	return req
}

func TestCacheSplit_LastMessageRollingBreakpointCountsToolContent(t *testing.T) {
	// 3 rounds x ~1750 tokens of tool output; string content alone is ~1 token,
	// so the old string-only estimate never reached the 2048 minimum.
	cfg := allCaching
	cfg.CacheSystem = false
	cfg.CacheTools = false
	body := sendAndCapture(t, cfg, toolLoopRequest(3, 1000))
	msgs := body["messages"].([]interface{})
	last := msgs[len(msgs)-1].(map[string]interface{})
	blocks := last["content"].([]interface{})
	tr := blocks[0].(map[string]interface{})
	guidance := blocks[len(blocks)-1].(map[string]interface{})
	if tr["type"] != "tool_result" {
		t.Fatalf("first block of last message = %v", tr["type"])
	}
	if _, ok := tr["cache_control"]; !ok {
		t.Errorf("last message's tool_result must carry the rolling breakpoint")
	}
	if _, ok := guidance["cache_control"]; ok {
		t.Errorf("ephemeral guidance text must not carry the breakpoint")
	}
}

func TestCacheSplit_LastMessageStringContentMarked(t *testing.T) {
	cfg := allCaching
	cfg.CacheSystem = false
	body := sendAndCapture(t, cfg, cachingTestRequest("", 10))
	msgs := body["messages"].([]interface{})
	last := msgs[len(msgs)-1].(map[string]interface{})
	blocks, ok := last["content"].([]interface{})
	if !ok || len(blocks) != 1 {
		t.Fatalf("last message content should become one text block, got %T", last["content"])
	}
	if _, ok := blocks[0].(map[string]interface{})["cache_control"]; !ok {
		t.Errorf("last message must carry a breakpoint")
	}
}

func TestCacheSplit_NeverMoreThanFourBreakpoints(t *testing.T) {
	req := toolLoopRequest(12, 500)
	req.Messages[0] = splitSystemMessage(t, strings.Repeat("s ", 10000), "dyn")
	for i := 0; i < 40; i++ {
		req.Tools = append(req.Tools, Tool{Name: "tool" + string(rune('A'+i)), Description: strings.Repeat("d", 400),
			Parameters: map[string]interface{}{"type": "object"}})
	}
	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, allCaching))
	if err != nil {
		t.Fatal(err)
	}
	p.isOAuth, p.authCfg = false, nil
	for _, withTTL := range []bool{false, true} {
		p.caching.ExtendedTTL = withTTL
		if _, err := p.GenerateResponse(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		n := countCacheControl(capturedRequest(t, raw))
		if n > maxCacheBreakpoints {
			t.Fatalf("%d cache_control markers, API limit is %d", n, maxCacheBreakpoints)
		}
		if n != 4 {
			t.Errorf("expected all 4 breakpoints (tools, system, last, anchor), got %d", n)
		}
	}
}

// Non-Anthropic providers get the joined system text in one system message.
func TestCacheSplit_OpenAIGetsJoinedSystemText(t *testing.T) {
	var got []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{
				"message": map[string]interface{}{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer server.Close()
	p, err := NewOpenAIProvider(config.ProviderConfig{Name: "zai", BaseURL: server.URL, Model: "glm"})
	if err != nil {
		t.Fatal(err)
	}
	req := &GenerateRequest{MaxTokens: 10, Messages: []ChatMessage{splitSystemMessage(t, "STATIC", "DYNAMIC"), {Role: "user", Content: "hi"}}}
	if _, err := p.GenerateResponse(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	systems := 0
	for _, m := range body.Messages {
		if m["role"] == "system" {
			systems++
			if m["content"] != "STATIC\n\nDYNAMIC" {
				t.Errorf("system content = %#v, want joined text", m["content"])
			}
		}
	}
	if systems != 1 {
		t.Errorf("want exactly 1 system message, got %d", systems)
	}
	if strings.Contains(string(got), "system_blocks") || strings.Contains(string(got), "cache_control") {
		t.Errorf("internal block fields leaked to OpenAI-compatible request: %s", got)
	}
}
