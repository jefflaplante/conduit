package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// conduit-31jg.12: the streaming path drifted from the non-streaming one
// (hardcoded host and max_tokens, no cache breakpoints, no OAuth refresh,
// dropped system messages, ignored SSE error events, dropped cache usage,
// unchecked type assertions). These tests pin parity.

const okSSE = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":12,"cache_creation_input_tokens":300,"cache_read_input_tokens":4000,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type":"ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`

// sseServer serves sse to every request and captures the last request.
func sseServer(t *testing.T, sse string) (*httptest.Server, func() (*http.Request, map[string]interface{})) {
	t.Helper()
	var mu sync.Mutex
	var lastReq *http.Request
	var lastBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		lastReq, lastBody = r.Clone(context.Background()), body
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(srv.Close)
	return srv, func() (*http.Request, map[string]interface{}) {
		mu.Lock()
		defer mu.Unlock()
		return lastReq, lastBody
	}
}

func TestStreaming_UsesBaseURLMaxTokensAndCacheBreakpoints(t *testing.T) {
	srv, last := sseServer(t, okSSE)
	p := newTestAnthropic(t, srv.URL) // caching defaults: enabled

	sys := strings.Repeat("system prompt text ", 1000) // clears cache min tokens
	req := cachingTestRequest(sys, 12)
	req.MaxTokens = 1234
	// A mid-conversation system message (e.g. goal refocus) must survive;
	// the old streaming path only kept messages[0].
	req.Messages = append(req.Messages, ChatMessage{Role: "system", Content: "REFOCUS-MARKER"})

	resp, err := p.GenerateResponseStreaming(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("GenerateResponseStreaming: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("content = %q", resp.Content)
	}

	httpReq, body := last()
	if httpReq == nil {
		t.Fatal("test server (baseURL) never received the streaming request")
	}
	if httpReq.URL.Path != "/v1/messages" {
		t.Errorf("path = %q", httpReq.URL.Path)
	}
	if got := httpReq.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}
	if mt, _ := body["max_tokens"].(float64); int(mt) != 1234 {
		t.Errorf("max_tokens = %v, want 1234", body["max_tokens"])
	}

	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), `"cache_control"`) {
		t.Error("streaming request has no cache_control breakpoints")
	}
	sysBlocks, ok := body["system"].([]interface{})
	if !ok {
		t.Fatalf("system should be a block array when cache markers are present, got %T", body["system"])
	}
	var sawRefocus bool
	for _, b := range sysBlocks {
		if m, ok := b.(map[string]interface{}); ok && m["text"] == "REFOCUS-MARKER" {
			sawRefocus = true
		}
	}
	if !sawRefocus {
		t.Error("mid-conversation system message was dropped from the streaming request")
	}
}

func TestStreaming_ZeroMaxTokensDefaults(t *testing.T) {
	srv, last := sseServer(t, okSSE)
	p := newTestAnthropic(t, srv.URL)
	if _, err := p.GenerateResponseStreaming(context.Background(), &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, nil); err != nil {
		t.Fatalf("GenerateResponseStreaming: %v", err)
	}
	_, body := last()
	if mt, _ := body["max_tokens"].(float64); int(mt) != defaultChainMaxTokens {
		t.Errorf("max_tokens = %v, want default %d", body["max_tokens"], defaultChainMaxTokens)
	}
}

func TestParseSSEStream_CacheUsage(t *testing.T) {
	p := &AnthropicProvider{model: "m"}
	resp, err := p.parseSSEStream(strings.NewReader(okSSE), nil)
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	u := resp.Usage
	if u.PromptTokens != 12 || u.CompletionTokens != 42 || u.TotalTokens != 54 {
		t.Errorf("usage tokens = %+v", u)
	}
	if u.CacheCreationInputTokens != 300 || u.CacheReadInputTokens != 4000 {
		t.Errorf("cache usage = write %d read %d, want 300/4000", u.CacheCreationInputTokens, u.CacheReadInputTokens)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
}

func TestParseSSEStream_ErrorEventReturnsError(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial ans"}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`
	p := &AnthropicProvider{model: "m"}
	resp, err := p.parseSSEStream(strings.NewReader(sse), nil)
	if err == nil {
		t.Fatal("mid-stream error event must return an error, got nil")
	}
	if !strings.Contains(err.Error(), "overloaded_error") {
		t.Errorf("error should carry the error type, got %q", err.Error())
	}
	if ClassifyError(err) != CategoryRateLimit {
		t.Errorf("overloaded stream error should classify as rate-limit, got %v", ClassifyError(err))
	}
	if resp == nil || !resp.Partial || resp.Content != "partial ans" {
		t.Errorf("expected Partial response with received text, got %+v", resp)
	}
}

func TestParseSSEStream_MalformedContentBlockStartNoPanic(t *testing.T) {
	sse := `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":123,"name":null}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":"not-a-map"}

data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","name":"Read"}}

data: {"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"ok"}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

`
	p := &AnthropicProvider{model: "m"}
	var resp *GenerateResponse
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseSSEStream panicked on malformed content_block_start: %v", r)
			}
		}()
		resp, err = p.parseSSEStream(strings.NewReader(sse), nil)
	}()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("malformed tool_use blocks must be skipped, got %+v", resp.ToolCalls)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

// The OAuth refresh must run on the streaming path, and the refreshed token
// (read under oauthMu) must be what goes on the wire.
func TestStreaming_RefreshesOAuthToken(t *testing.T) {
	var refreshCalls int
	var mu sync.Mutex
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		refreshCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "sk-ant-oat01-REFRESHED",
			"refresh_token": "refresh-2",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(tokenSrv.Close)

	dataDir := t.TempDir()
	t.Setenv("HOME", dataDir)
	t.Setenv("CONDUIT_DATA_DIR", dataDir)
	t.Setenv("ANTHROPIC_OAUTH_TOKEN_URL", tokenSrv.URL)

	srv, last := sseServer(t, okSSE)
	p := &AnthropicProvider{
		name:    "anthropic",
		apiKey:  "sk-ant-oat01-EXPIRED",
		model:   "claude-sonnet-4-6",
		client:  &http.Client{Timeout: 10 * time.Second},
		baseURL: srv.URL,
		isOAuth: true,
		caching: config.DefaultPromptCachingConfig(),
		authCfg: &config.AuthConfig{
			Type:         "oauth",
			OAuthToken:   "sk-ant-oat01-EXPIRED",
			RefreshToken: "refresh-1",
			ExpiresAt:    time.Now().Add(-time.Hour).Unix(),
			ClientID:     "test-client",
		},
	}

	if _, err := p.GenerateResponseStreaming(context.Background(), &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}}, MaxTokens: 10,
	}, nil); err != nil {
		t.Fatalf("GenerateResponseStreaming: %v", err)
	}

	mu.Lock()
	calls := refreshCalls
	mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected 1 OAuth refresh on the streaming path, got %d", calls)
	}
	httpReq, _ := last()
	if got := httpReq.Header.Get("Authorization"); got != "Bearer sk-ant-oat01-REFRESHED" {
		t.Errorf("Authorization = %q, want refreshed token", got)
	}
}

// Router streaming path now reports to the usage tracker (success + error).
func TestRouterStreaming_RecordsUsage(t *testing.T) {
	router, mock := newUsageTestRouter(t, nil)
	mock.SetResponses([]MockResponse{
		{Content: "hi", Usage: Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10, CacheReadInputTokens: 50}},
		{Error: errors.New("boom")},
	})

	if _, err := router.GenerateResponseStreaming(context.Background(), &sessions.Session{}, "x", "mock", "", nil); err != nil {
		t.Fatalf("stream 1: %v", err)
	}
	if _, err := router.GenerateResponseStreaming(context.Background(), &sessions.Session{}, "x", "mock", "", nil); err == nil {
		t.Fatal("stream 2: expected error")
	}

	rec, ok := router.GetUsageTracker().GetProviderUsage("mock")
	if !ok {
		t.Fatal("no usage recorded for streaming provider")
	}
	if rec.TotalRequests < 1 || rec.TotalInputTokens != 7 || rec.TotalCacheReadTokens != 50 {
		t.Errorf("usage record = %+v", rec)
	}
	if rec.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", rec.ErrorCount)
	}
}
