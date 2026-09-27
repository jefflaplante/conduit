package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
)

// Golden wire-format equivalence tests (conduit-31jg.36).
//
// The fixtures under testdata/anthropic_wire/ were recorded from the
// map[string]interface{} implementation of the Anthropic provider BEFORE it
// was converted to typed request/response structs. They pin:
//
//   - requests/*.json: the exact request body (decoded, so key order does not
//     matter) and auth headers sent by GenerateResponse and
//     GenerateResponseStreaming for a matrix of representative requests;
//   - responses/*.json: the GenerateResponse (and error) produced from
//     recorded non-streaming replies and SSE streams.
//
// Re-record (only when an intentional wire change is made) with:
//
//	UPDATE_ANTHROPIC_GOLDEN=1 go test -run TestAnthropicWireGolden ./internal/ai/

const goldenDir = "testdata/anthropic_wire"

func updateGolden() bool { return os.Getenv("UPDATE_ANTHROPIC_GOLDEN") == "1" }

// goldenCompare writes got to path when updating, otherwise compares it with
// the stored fixture as decoded JSON.
func goldenCompare(t *testing.T, path string, got interface{}) {
	t.Helper()
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden value: %v", err)
	}
	full := filepath.Join(goldenDir, path)
	if updateGolden() {
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, append(gotJSON, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	wantJSON, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read golden %s: %v (record with UPDATE_ANTHROPIC_GOLDEN=1)", full, err)
	}
	var want, have interface{}
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatalf("decode golden %s: %v", full, err)
	}
	if err := json.Unmarshal(gotJSON, &have); err != nil {
		t.Fatalf("decode current: %v", err)
	}
	if !reflect.DeepEqual(want, have) {
		t.Errorf("%s: wire output differs from golden fixture\n--- want\n%s\n--- got\n%s", full, wantJSON, gotJSON)
	}
}

// ---------------------------------------------------------------------------
// Request matrix
// ---------------------------------------------------------------------------

type goldenRequestCase struct {
	name    string
	oauth   bool
	caching config.PromptCachingConfig
	model   string // provider default model
	req     *GenerateRequest
}

var goldenAllCaching = config.PromptCachingConfig{
	Enabled: true, CacheTools: true, CacheSystem: true, CacheHistory: true,
	HistoryBreakpointInterval: 6,
}

func goldenBigTools() []Tool {
	return []Tool{
		{Name: "Read", Description: "Read a file. " + strings.Repeat("read docs ", 400), Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string", "description": "<path> & more"}},
			"required":   []interface{}{"path"},
		}},
		{Name: "custom_tool", Description: "Not a Claude Code tool", Parameters: map[string]interface{}{"type": "object"}},
		{Name: "Bash", Description: "Run a command. " + strings.Repeat("bash docs ", 400), Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
		}},
		{Name: "NoSchema", Description: "", Parameters: nil},
	}
}

func goldenToolRound() []ChatMessage {
	return []ChatMessage{
		{Role: "user", Content: "list files and read one"},
		{Role: "assistant", Content: "Let me look.", ToolCalls: []ToolCall{
			{ID: "toolu_1", Name: "Bash", Args: map[string]interface{}{"command": "ls"}},
			{ID: "toolu_2", Name: "Read", Args: nil},
		}},
		{Role: "tool", ToolCallID: "toolu_1", Content: "a.txt\nb.txt"},
		{Role: "tool", ToolCallID: "toolu_2", Content: "error: missing path", IsError: true},
		{Role: "user", Content: "[guidance] stay focused"},
	}
}

func goldenLongHistory(n int, filler string) []ChatMessage {
	var msgs []ChatMessage
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			ChatMessage{Role: "user", Content: fmt.Sprintf("question %d %s", i, filler)},
			ChatMessage{Role: "assistant", Content: fmt.Sprintf("answer %d %s", i, filler)},
		)
	}
	return append(msgs, ChatMessage{Role: "user", Content: "final question"})
}

func goldenRequestCases() []goldenRequestCase {
	bigStatic := strings.Repeat("static instructions ", 1000) // ~5000 tokens
	dynamic := "## Time Context\nCurrent time: Sat 2026-09-26 21:14 PDT"
	split := ChatMessage{
		Role:    "system",
		Content: bigStatic + "\n\n" + dynamic,
		SystemBlocks: []SystemBlock{
			{Type: "text", Text: bigStatic},
			{Type: "text", Text: dynamic, Dynamic: true},
		},
	}
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	filler := strings.Repeat("history filler ", 120)

	return []goldenRequestCase{
		{
			name: "plain_apikey_no_cache",
			req: &GenerateRequest{MaxTokens: 256, Messages: []ChatMessage{
				{Role: "system", Content: "You are helpful."},
				{Role: "user", Content: "hi <b>&</b>"},
			}},
		},
		{
			name:    "plain_apikey_cache_below_threshold",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 256, Messages: []ChatMessage{
				{Role: "system", Content: "You are helpful."},
				{Role: "user", Content: "hi"},
			}},
		},
		{
			name: "zero_max_tokens_model_override_no_system",
			req: &GenerateRequest{Model: "claude-haiku-4-5", Messages: []ChatMessage{
				{Role: "user", Content: "hi"},
			}},
		},
		{
			name:    "empty_messages",
			caching: goldenAllCaching,
			req:     &GenerateRequest{MaxTokens: 10},
		},
		{
			name:    "tools_apikey_cache_all",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 1000, Tools: goldenBigTools(), Messages: append(
				[]ChatMessage{{Role: "system", Content: bigStatic}}, goldenToolRound()...)},
		},
		{
			name:  "tools_oauth_no_cache",
			oauth: true,
			req: &GenerateRequest{MaxTokens: 1000, Tools: goldenBigTools(), Messages: []ChatMessage{
				{Role: "system", Content: "You are helpful."},
				{Role: "user", Content: "hi"},
			}},
		},
		{
			name:    "tools_oauth_cache_all",
			oauth:   true,
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 1000, Tools: goldenBigTools(), Messages: append(
				[]ChatMessage{split}, goldenToolRound()...)},
		},
		{
			name:  "oauth_only_unsupported_tools",
			oauth: true,
			req: &GenerateRequest{MaxTokens: 100, Tools: []Tool{{Name: "custom_tool", Parameters: map[string]interface{}{"type": "object"}}},
				Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		},
		{
			name: "tool_result_is_error_no_cache",
			req:  &GenerateRequest{MaxTokens: 500, Messages: goldenToolRound()},
		},
		{
			name: "tool_rounds_multi_and_empty_results",
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				{Role: "user", Content: "go"},
				{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "Bash", Args: map[string]interface{}{"command": "true"}}}},
				{Role: "tool", ToolCallID: "t1", Content: ""},
				{Role: "assistant", Content: "", ToolCalls: []ToolCall{
					{ID: "t2", Name: "Read", Args: map[string]interface{}{"path": "x"}},
					{ID: "t3", Name: "Read", Args: map[string]interface{}{}},
				}},
				{Role: "tool", ToolCallID: "t2", Content: "x contents"},
				{Role: "tool", ToolCallID: "t3", Content: "boom", IsError: true},
				{Role: "user", Content: ""},
				{Role: "assistant", Content: ""},
				{Role: "user", Content: "thanks"},
			}},
		},
		{
			name:    "tool_result_trailing_guidance_cache",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 500, Messages: append(
				[]ChatMessage{{Role: "system", Content: bigStatic}}, goldenToolRound()...)},
		},
		{
			name:    "extended_ttl",
			caching: config.PromptCachingConfig{Enabled: true, CacheTools: true, CacheSystem: true, CacheHistory: true, ExtendedTTL: true},
			req: &GenerateRequest{MaxTokens: 500, Tools: goldenBigTools(), Messages: append(
				[]ChatMessage{split}, goldenLongHistory(8, filler)...)},
		},
		{
			name:    "system_split_dynamic_apikey",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				split,
				{Role: "user", Content: "hi"},
			}},
		},
		{
			name: "system_split_dynamic_no_cache",
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				split,
				{Role: "user", Content: "hi"},
			}},
		},
		{
			name:    "system_split_mismatch_and_mid_conversation_system",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				{Role: "system", Content: bigStatic + " rewritten", SystemBlocks: split.SystemBlocks},
				{Role: "user", Content: "hi"},
				{Role: "system", Content: "Mid-conversation note."},
				{Role: "system", Content: ""},
				{Role: "assistant", Content: "hello"},
				{Role: "user", Content: "again"},
			}},
		},
		{
			name:    "system_blocks_with_empty_static",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				{Role: "system", Content: "\n\n" + bigStatic, SystemBlocks: []SystemBlock{
					{Type: "text", Text: ""}, {Type: "text", Text: bigStatic, Dynamic: true},
				}},
				{Role: "user", Content: "hi"},
			}},
		},
		{
			name:    "oauth_system_split_small",
			oauth:   true,
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				{Role: "system", Content: "static\n\ndyn", SystemBlocks: []SystemBlock{
					{Type: "text", Text: "static"}, {Type: "text", Text: "dyn", Dynamic: true},
				}},
				{Role: "user", Content: "hi"},
			}},
		},
		{
			name:    "images",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				{Role: "system", Content: bigStatic},
				{Role: "user", Content: "what is this?", Attachments: []Attachment{
					{Type: "image", MediaType: "image/png", Data: png},
					{Type: "audio", MediaType: "audio/ogg", Data: []byte{1}},
					{Type: "image", MediaType: "image/jpeg"},
				}},
				{Role: "assistant", Content: "a picture"},
				{Role: "user", Attachments: []Attachment{{Type: "image", MediaType: "image/png", Data: png}}},
			}},
		},
		{
			name: "image_attachment_dropped_when_empty",
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				{Role: "user", Content: "first"},
				{Role: "user", Attachments: []Attachment{{Type: "document", MediaType: "application/pdf", Data: []byte{1}}}},
			}},
		},
		{
			name: "image_after_tool_results_merges",
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				{Role: "user", Content: "look"},
				{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "Read", Args: map[string]interface{}{"path": "p.png"}}}},
				{Role: "tool", ToolCallID: "t1", Content: "image loaded"},
				{Role: "user", Content: "here it is", Attachments: []Attachment{{Type: "image", MediaType: "image/png", Data: png}}},
			}},
		},
		{
			name:    "history_anchor_four_breakpoints",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 500, Tools: goldenBigTools(), Messages: append(
				[]ChatMessage{split}, goldenLongHistory(10, filler)...)},
		},
		{
			name:    "history_anchor_interval_default",
			caching: config.PromptCachingConfig{Enabled: true, CacheHistory: true},
			req:     &GenerateRequest{MaxTokens: 500, Messages: goldenLongHistory(10, filler)},
		},
		{
			name:    "cache_granular_system_only",
			caching: config.PromptCachingConfig{Enabled: true, CacheSystem: true},
			req: &GenerateRequest{MaxTokens: 500, Tools: goldenBigTools(), Messages: append(
				[]ChatMessage{{Role: "system", Content: bigStatic}}, goldenLongHistory(10, filler)...)},
		},
		{
			name:    "cache_granular_tools_history",
			caching: config.PromptCachingConfig{Enabled: true, CacheTools: true, CacheHistory: true, HistoryBreakpointInterval: 2},
			req: &GenerateRequest{MaxTokens: 500, Tools: goldenBigTools(), Messages: append(
				[]ChatMessage{{Role: "system", Content: "short"}}, goldenLongHistory(4, filler)...)},
		},
		{
			name:    "cache_last_message_empty_string",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 500, Messages: []ChatMessage{
				{Role: "system", Content: bigStatic},
				{Role: "user", Content: filler + filler},
				{Role: "assistant", Content: ""},
			}},
		},
		{
			name:    "cache_haiku_threshold",
			model:   "claude-haiku-4-5",
			caching: goldenAllCaching,
			req: &GenerateRequest{MaxTokens: 500, Tools: goldenBigTools(), Messages: append(
				[]ChatMessage{{Role: "system", Content: strings.Repeat("s ", 3000)}}, goldenLongHistory(3, filler)...)},
		},
	}
}

// wireCapture records every request the fake API receives.
type wireCapture struct {
	mu   sync.Mutex
	reqs []capturedWire
}

type capturedWire struct {
	Headers map[string]string `json:"headers"`
	Body    interface{}       `json:"body"`
	raw     []byte
}

var goldenHeaderNames = []string{
	"Content-Type", "Accept", "anthropic-version", "anthropic-beta",
	"anthropic-dangerous-direct-browser-access", "user-agent", "x-app",
	"x-api-key", "Authorization",
}

func (c *wireCapture) record(r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body interface{}
	_ = json.Unmarshal(raw, &body)
	body = compactLongStrings(body)
	h := map[string]string{}
	for _, name := range goldenHeaderNames {
		if v := r.Header.Get(name); v != "" {
			h[name] = v
		}
	}
	c.mu.Lock()
	c.reqs = append(c.reqs, capturedWire{Headers: h, Body: body, raw: raw})
	c.mu.Unlock()
}

func (c *wireCapture) all() []capturedWire {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedWire(nil), c.reqs...)
}

// compactLongStrings replaces long strings in a decoded body with their
// length, prefix and SHA-256 so fixtures stay small while any byte change
// still fails the comparison.
func compactLongStrings(v interface{}) interface{} {
	switch x := v.(type) {
	case string:
		if len(x) <= 200 {
			return x
		}
		sum := sha256.Sum256([]byte(x))
		return fmt.Sprintf("<len=%d sha256=%x prefix=%q>", len(x), sum[:8], x[:40])
	case map[string]interface{}:
		for k, vv := range x {
			x[k] = compactLongStrings(vv)
		}
	case []interface{}:
		for i, vv := range x {
			x[i] = compactLongStrings(vv)
		}
	}
	return v
}

const goldenOKJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

const goldenOKSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func goldenProvider(url string, oauth bool, caching config.PromptCachingConfig, model string) *AnthropicProvider {
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	key := "sk-ant-api03-golden"
	if oauth {
		key = "sk-ant-oat01-golden"
	}
	return &AnthropicProvider{
		name:    "anthropic",
		apiKey:  key,
		model:   model,
		client:  &http.Client{Timeout: 10 * time.Second},
		baseURL: url,
		isOAuth: oauth,
		caching: caching,
		retry:   retryPolicy{maxRetries: 2, baseDelay: time.Millisecond, maxDelay: 5 * time.Millisecond},
	}
}

func TestAnthropicWireGolden_Requests(t *testing.T) {
	for _, tc := range goldenRequestCases() {
		t.Run(tc.name, func(t *testing.T) {
			capture := &wireCapture{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capture.record(r)
				if strings.Contains(r.Header.Get("Accept")+r.Header.Get("accept"), "event-stream") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, goldenOKSSE)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, goldenOKJSON)
			}))
			defer srv.Close()
			p := goldenProvider(srv.URL, tc.oauth, tc.caching, tc.model)

			if _, err := p.GenerateResponse(context.Background(), tc.req); err != nil {
				t.Fatalf("GenerateResponse: %v", err)
			}
			if _, err := p.GenerateResponseStreaming(context.Background(), tc.req, nil); err != nil {
				t.Fatalf("GenerateResponseStreaming: %v", err)
			}
			reqs := capture.all()
			if len(reqs) != 2 {
				t.Fatalf("captured %d requests, want 2", len(reqs))
			}
			goldenCompare(t, "requests/"+tc.name+".json", map[string]interface{}{
				"non_streaming": reqs[0],
				"streaming":     reqs[1],
			})
		})
	}
}

// conduit-31jg.46: every retry attempt must send the byte-identical body.
func TestAnthropicWireGolden_RetrySendsUnchangedBody(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			capture := &wireCapture{}
			n := 0
			var mu sync.Mutex
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capture.record(r)
				mu.Lock()
				n++
				attempt := n
				mu.Unlock()
				if attempt < 3 {
					w.WriteHeader(529)
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, goldenOKSSE)
					return
				}
				_, _ = io.WriteString(w, goldenOKJSON)
			}))
			defer srv.Close()
			tc := goldenRequestCases()[4] // tools_apikey_cache_all: tools + system + tool round
			p := goldenProvider(srv.URL, false, tc.caching, "")
			var err error
			if stream {
				_, err = p.GenerateResponseStreaming(context.Background(), tc.req, nil)
			} else {
				_, err = p.GenerateResponse(context.Background(), tc.req)
			}
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			reqs := capture.all()
			if len(reqs) != 3 {
				t.Fatalf("attempts = %d, want 3", len(reqs))
			}
			for i := 1; i < len(reqs); i++ {
				if !bytes.Equal(reqs[0].raw, reqs[i].raw) {
					t.Errorf("attempt %d body differs from attempt 1", i+1)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Response matrix
// ---------------------------------------------------------------------------

type goldenResult struct {
	Resp          *GenerateResponse `json:"resp"`
	Err           string            `json:"err,omitempty"`
	StreamErrType string            `json:"stream_err_type,omitempty"`
	Deltas        []string          `json:"deltas,omitempty"`
	DoneCalls     int               `json:"done_calls,omitempty"`
}

func goldenResultOf(resp *GenerateResponse, err error, deltas []string, done int) goldenResult {
	r := goldenResult{Resp: resp, Deltas: deltas, DoneCalls: done}
	if err != nil {
		r.Err = err.Error()
		if se, ok := asAnthropicStreamError(err); ok {
			r.StreamErrType = se.Type
		}
	}
	return r
}

var goldenJSONResponses = map[string]string{
	"text_end_turn":            `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"Hello there"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`,
	"multi_text_and_empty":     `{"model":"claude-sonnet-4-6","content":[{"type":"text","text":""},{"type":"text","text":"a"},{"type":"text","text":""},{"type":"text"},{"type":"text","text":"b"}],"stop_reason":"stop_sequence","usage":{"input_tokens":1,"output_tokens":2}}`,
	"text_and_tool_use":        `{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls","n":3,"nested":{"a":[1,2]}}}],"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":5000,"cache_read_input_tokens":0}}`,
	"max_tokens_trailing_tool": `{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"path":"a"}},{"type":"tool_use","id":"t2","name":"Read","input":{}}],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`,
	"max_tokens_trailing_text": `{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"path":"a"}},{"type":"text","text":"and then"}],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`,
	"context_window_exceeded":  `{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}],"stop_reason":"model_context_window_exceeded","usage":{"input_tokens":1,"output_tokens":1}}`,
	"refusal_with_tool":        `{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"rm -rf /"}}],"stop_reason":"refusal","usage":{"input_tokens":1,"output_tokens":1}}`,
	"refusal_with_text":        `{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"I can't help with that."}],"stop_reason":"refusal","usage":{"input_tokens":1,"output_tokens":1}}`,
	"pause_turn":               `{"model":"claude-sonnet-4-6","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"x"}}],"stop_reason":"pause_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
	"unknown_blocks_tolerated": `{"model":"claude-sonnet-4-6","content":[{"type":"thinking","thinking":"hmm","signature":"sig"},{"type":"redacted_thinking","data":"xx"},{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q"}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://x","title":"X"}]},{"type":"text","text":"answer","citations":[{"type":"web_search_result_location","url":"https://x"}]},{"type":"future_block","foo":1}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1,"server_tool_use":{"web_search_requests":1}}}`,
	"malformed_tool_use":       `{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"t1","name":"Read","input":"not-an-object"},{"type":"tool_use","id":7,"name":"Read","input":{}},{"type":"tool_use","id":"t3","input":{}},{"type":"tool_use","id":"t4","name":"Read","input":null},{"type":"tool_use","id":"","name":"","input":{}},"not-a-block",{"type":5,"text":"x"},{"type":"tool_use","id":"t6","name":"Read","input":{"ok":true}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`,
	"last_block_not_object":    `{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}},"x"],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`,
	"usage_odd_numbers":        `{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"x"}],"stop_reason":"end_turn","usage":{"input_tokens":10.9,"output_tokens":"5","cache_creation_input_tokens":null,"cache_read_input_tokens":3}}`,
	"usage_missing":            `{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"x"}],"stop_reason":"end_turn"}`,
	"usage_not_object":         `{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"x"}],"stop_reason":"end_turn","usage":[1,2]}`,
	"content_not_array":        `{"model":"claude-sonnet-4-6","content":"hello","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
	"stop_reason_non_string":   `{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"x"}],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}`,
	"stop_reason_unknown":      `{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"x"}],"stop_reason":"brand_new_reason","usage":{"input_tokens":1,"output_tokens":1}}`,
	"model_mismatch_kept":      `{"model":"claude-opus-4-1","content":[{"type":"text","text":"from another model"},{"type":"tool_use","id":"t1","name":"Read","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`,
	"model_non_string":         `{"model":42,"content":[{"type":"text","text":"x"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
	"null_body":                `null`,
	"empty_object":             `{}`,
	"trailing_garbage":         `{"content":[{"type":"text","text":"x"}],"stop_reason":"end_turn"} trailing`,
	"invalid_json":             `{"content":[`,
	"array_body":               `[1,2,3]`,
}

func TestAnthropicWireGolden_JSONResponses(t *testing.T) {
	for name, body := range goldenJSONResponses {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()
			p := goldenProvider(srv.URL, false, config.PromptCachingConfig{}, "")
			resp, err := p.GenerateResponse(context.Background(), &GenerateRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
			goldenCompare(t, "responses/json_"+name+".json", goldenResultOf(resp, err, nil, 0))
		})
	}
}

func TestAnthropicWireGolden_HTTPErrors(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"400_invalid_request": {400, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: required"}}`},
		"401_auth":            {401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`},
		"529_exhausted":       {529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
		"429_quota":           {429, `{"type":"error","error":{"type":"rate_limit_error","message":"You have reached your specified API usage limits"}}`},
	}
	for name, c := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", name, stream), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(c.status)
					_, _ = io.WriteString(w, c.body)
				}))
				defer srv.Close()
				p := goldenProvider(srv.URL, false, config.PromptCachingConfig{}, "")
				req := &GenerateRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
				var resp *GenerateResponse
				var err error
				if stream {
					resp, err = p.GenerateResponseStreaming(context.Background(), req, nil)
				} else {
					resp, err = p.GenerateResponse(context.Background(), req)
				}
				goldenCompare(t, fmt.Sprintf("responses/http_%s_stream_%v.json", name, stream), goldenResultOf(resp, err, nil, 0))
			})
		}
	}
}

func sseOf(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: ")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return b.String()
}

var goldenSSEStreams = map[string]string{
	"ok_text_cache_usage": okSSE,
	"tool_use_complete": sseOf(
		`{"type":"message_start","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":20,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me check."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"l"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"s\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"Glob","input":{}}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":30}}`,
		`{"type":"message_stop"}`,
	),
	"truncated_tool_use": sseOf(
		`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Read"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t2","name":"Write"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"b\",\"cont"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":100}}`,
		`{"type":"message_stop"}`,
	),
	"invalid_tool_json_end_turn": sseOf(
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Read"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"[1,2]"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
	),
	"refusal_stream": sseOf(
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Bash"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":1}}`,
	),
	"error_event_after_text": `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial ans"}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`,
	"error_event_no_text":        sseOf(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`),
	"error_event_no_error_obj":   sseOf(`{"type":"error"}`),
	"error_event_odd_fields":     sseOf(`{"type":"error","error":{"type":"","message":5}}`),
	"error_event_non_string_typ": sseOf(`{"type":"error","error":{"type":7,"message":"m"}}`),
	"error_after_tool_call": sseOf(
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Read"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"error","error":{"type":"api_error","message":"boom"}}`,
	),
	"malformed_blocks": `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":123,"name":null}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":"not-a-map"}

data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","name":"Read"}}

data: {"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"ok"}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

`,
	"malformed_block_last_is_tool_use_max_tokens": sseOf(
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Read"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"","name":"Read"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`,
	),
	"not_a_map_block_keeps_last_type": sseOf(
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"Read"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":null}`,
		`{"type":"content_block_start","index":2,"content_block":[1]}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`,
	),
	"odd_deltas_and_events": "event: message_start\n" +
		"data: not json\n\n" +
		"data:\n\n" +
		"data: [DONE]\n\n" +
		": comment line\n\n" +
		"data: 123\n\n" +
		"data: null\n\n" +
		`data: {"type":7}` + "\n\n" +
		`data:{"type":"message_start","message":{"usage":{"input_tokens":4.7,"output_tokens":"x","cache_read_input_tokens":2}}}` + "\n\n" +
		`data: {"type":"message_start","message":{"usage":"nope"}}` + "\n\n" +
		`data: {"type":"message_start","message":"nope"}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":5}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":"x"}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"A"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s"}}` + "\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{}}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"ping"}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9,"input_tokens":0}}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":11,"cache_creation_input_tokens":6}}` + "\n\n" +
		`data: {"type":"message_delta","delta":"x","usage":"y"}` + "\n\n" +
		`data: {"type":"message_stop"}` + "\n\n" +
		`data: {"type":"message_stop"}` + "\n\n",
	"stop_reason_empty_string_overrides": sseOf(
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`,
		`{"type":"message_delta","delta":{"stop_reason":""}}`,
	),
	"thinking_and_server_tool_blocks": sseOf(
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","usage":{"input_tokens":3,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srv1","name":"web_search","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"x\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv1","content":[{"type":"web_search_result","url":"u"}]}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"done"}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	),
	"eof_without_stop": sseOf(
		`{"type":"message_start","message":{"usage":{"input_tokens":2}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`,
	),
	"empty_stream": "",
}

// goldenErrReader yields data then a read error (connection reset).
type goldenErrReader struct {
	r   io.Reader
	err error
}

func (g *goldenErrReader) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	if err == io.EOF {
		return n, g.err
	}
	return n, err
}

func TestAnthropicWireGolden_SSEStreams(t *testing.T) {
	for name, sse := range goldenSSEStreams {
		t.Run(name, func(t *testing.T) {
			p := goldenProvider("", false, config.PromptCachingConfig{}, "")
			var deltas []string
			done := 0
			resp, err := p.parseSSEStream(strings.NewReader(sse), func(d string, isDone bool) {
				if isDone {
					done++
					return
				}
				deltas = append(deltas, d)
			})
			goldenCompare(t, "responses/sse_"+name+".json", goldenResultOf(resp, err, deltas, done))
		})
	}
	t.Run("read_error_mid_stream", func(t *testing.T) {
		p := goldenProvider("", false, config.PromptCachingConfig{}, "")
		r := &goldenErrReader{r: strings.NewReader(goldenSSEStreams["eof_without_stop"]), err: errors.New("connection reset")}
		resp, err := p.parseSSEStream(r, nil)
		goldenCompare(t, "responses/sse_read_error_mid_stream.json", goldenResultOf(resp, err, nil, 0))
	})
	t.Run("line_too_long", func(t *testing.T) {
		p := goldenProvider("", false, config.PromptCachingConfig{}, "")
		sse := goldenSSEStreams["eof_without_stop"] + "data: " + strings.Repeat("x", maxSSELineBytes+10) + "\n\n"
		resp, err := p.parseSSEStream(strings.NewReader(sse), nil)
		goldenCompare(t, "responses/sse_line_too_long.json", goldenResultOf(resp, err, nil, 0))
	})
}

// Full streaming path including retry of a mid-stream overloaded error that
// arrives before any text, then success.
func TestAnthropicWireGolden_StreamingEndToEnd(t *testing.T) {
	streams := []string{
		goldenSSEStreams["error_event_no_text"],
		sseOf(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`),
		goldenSSEStreams["tool_use_complete"],
	}
	cases := map[string][]string{
		"non_retryable_error": streams[:1],
		"retry_then_tools":    streams[1:],
		"error_after_text":    {goldenSSEStreams["error_event_after_text"]},
	}
	for name, replies := range cases {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			n := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				i := n
				n++
				mu.Unlock()
				if i >= len(replies) {
					i = len(replies) - 1
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, replies[i])
			}))
			defer srv.Close()
			p := goldenProvider(srv.URL, false, config.PromptCachingConfig{}, "")
			var deltas []string
			done := 0
			resp, err := p.GenerateResponseStreaming(context.Background(),
				&GenerateRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
				func(d string, isDone bool) {
					if isDone {
						done++
						return
					}
					deltas = append(deltas, d)
				})
			goldenCompare(t, "responses/stream_e2e_"+name+".json", goldenResultOf(resp, err, deltas, done))
		})
	}
}
