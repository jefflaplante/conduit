package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"conduit/internal/ai"
	"conduit/internal/redact"
	"conduit/internal/tools/debuglog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-3kgo: the tool loop records LLM request/response entries.

const debugLogTestSecret = "dbg-secret-3kgo-XYZZY-0123456789"

func newDebugLogEngine(t *testing.T) (*ExecutionEngine, *debuglog.RingBuffer) {
	t.Helper()
	redact.RegisterSecret(debugLogTestSecret)
	reg := NewMockRegistry()
	reg.AddTool(&MockTool{name: "ok_tool", executeFunc: func(context.Context, map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{Success: true, Content: "result with " + debugLogTestSecret + " " + strings.Repeat("r", 5000)}, nil
	}})
	e := NewExecutionEngine(reg, 1, 0, 5)
	buf := debuglog.NewRingBuffer(100)
	e.SetDebugBuffer(buf)
	return e, buf
}

func entriesOf(buf *debuglog.RingBuffer, typ debuglog.EntryType) []debuglog.Entry {
	return buf.Entries(func(e debuglog.Entry) bool { return e.Type == typ })
}

func TestToolLoop_RecordsLLMEntries(t *testing.T) {
	e, buf := newDebugLogEngine(t)
	final := &ai.GenerateResponse{
		Content:    "final " + debugLogTestSecret + " " + strings.Repeat("é", 1000),
		StopReason: "end_turn",
		Usage:      ai.Usage{PromptTokens: 42, CompletionTokens: 7},
	}
	p := &scriptedProvider{t: t, script: []scriptStep{{resp: final}}}
	req := &ai.GenerateRequest{Model: "test-model", Messages: []ai.ChatMessage{{Role: "user", Content: "hi"}}, MaxTokens: 1000}

	_, err := e.HandleToolCallFlow(context.Background(), p, req, tcResp(10, "", "ok_tool"))
	require.NoError(t, err)

	resps := entriesOf(buf, debuglog.EntryLLMResponse)
	require.Len(t, resps, 2, "initial + post-tools responses")
	assert.Equal(t, "initial", resps[0].Meta["phase"])
	assert.Equal(t, "1", resps[0].Meta["tool_calls"])
	assert.Equal(t, "ok_tool", resps[0].Meta["tool_names"])

	post := resps[1]
	assert.Equal(t, "post_tools", post.Meta["phase"])
	assert.Equal(t, "model=test-model stop=end_turn", post.Result)
	assert.Equal(t, "42", post.Meta["prompt_tokens"])
	assert.Equal(t, "7", post.Meta["completion_tokens"])
	assert.Equal(t, "0", post.Meta["tool_calls"])
	assert.Positive(t, post.Duration)

	reqs := entriesOf(buf, debuglog.EntryLLMRequest)
	require.Len(t, reqs, 1)
	assert.Equal(t, "model=test-model", reqs[0].Result)
	assert.Equal(t, "tool", reqs[0].Meta["last_role"])
	assert.Equal(t, "0", reqs[0].Meta["depth"])
	assert.Equal(t, "1000", reqs[0].Meta["max_tokens"])

	// Previews are redacted and bounded; nothing holds the full text.
	for _, en := range append(reqs, resps...) {
		for k, v := range en.Meta {
			assert.NotContains(t, v, debugLogTestSecret, "meta %s leaks the secret", k)
			assert.LessOrEqual(t, utf8.RuneCountInString(v), debuglog.PreviewLen+1, "meta %s not truncated", k)
		}
	}
	assert.Contains(t, post.Meta["content_preview"], redact.Placeholder)
	assert.Contains(t, reqs[0].Meta["last_preview"], redact.Placeholder)
	assert.True(t, strings.HasSuffix(post.Meta["content_preview"], "…"))
	assert.True(t, utf8.ValidString(post.Meta["content_preview"]))

	assert.Empty(t, entriesOf(buf, debuglog.EntryThinking), "providers carry no thinking content")
}

func TestToolLoop_RecordsLLMError(t *testing.T) {
	e, buf := newDebugLogEngine(t)
	p := &scriptedProvider{t: t, script: []scriptStep{{err: errors.New("upstream said " + debugLogTestSecret)}}}
	req := &ai.GenerateRequest{Messages: []ai.ChatMessage{{Role: "user", Content: "hi"}}}

	_, err := e.HandleToolCallFlow(context.Background(), p, req, tcResp(10, "", "ok_tool"))
	require.Error(t, err)

	resps := entriesOf(buf, debuglog.EntryLLMResponse)
	require.Len(t, resps, 2)
	failed := resps[1]
	assert.Equal(t, "model=scripted:default stop=error", failed.Result)
	assert.Contains(t, failed.Error, "upstream said")
	assert.NotContains(t, failed.Error, debugLogTestSecret)
}

func TestToolLoop_NoDebugBufferIsNoop(t *testing.T) {
	e, _ := newDebugLogEngine(t)
	e.SetDebugBuffer(nil)
	p := &scriptedProvider{t: t, script: []scriptStep{{resp: &ai.GenerateResponse{Content: "done"}}}}
	req := &ai.GenerateRequest{Messages: []ai.ChatMessage{{Role: "user", Content: "hi"}}}
	_, err := e.HandleToolCallFlow(context.Background(), p, req, tcResp(10, "", "ok_tool"))
	require.NoError(t, err)
}
