package ai

import (
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseClaudeCodeStream_BasicTextStreaming(t *testing.T) {
	stream := lines(
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "Hello"}}}`,
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": " world"}}}`,
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "!"}}}`,
	)

	var deltas []string
	result, err := ParseClaudeCodeStream(strings.NewReader(stream), func(delta string, done bool) {
		if !done {
			deltas = append(deltas, delta)
		}
	})

	require.NoError(t, err)
	assert.Equal(t, "Hello world!", result.Content)
	assert.Equal(t, []string{"Hello", " world", "!"}, deltas)
	assert.False(t, result.Partial)
}

func TestParseClaudeCodeStream_ToolUseEvents(t *testing.T) {
	stream := lines(
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "Let me check."}}}`,
		`{"type": "message", "content": [{"type": "tool_use", "name": "Read", "input": {"file": "foo.go"}}]}`,
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": " Done."}}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "Let me check. Done.", result.Content)
	assert.False(t, result.Partial)
}

func TestParseClaudeCodeStream_RetryEvents(t *testing.T) {
	stream := lines(
		`{"type": "system/api_retry", "attempt": 1, "max_retries": 3, "retry_delay_ms": 1000}`,
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "Recovered."}}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "Recovered.", result.Content)
	assert.False(t, result.Partial)
}

func TestParseClaudeCodeStream_PartialStream(t *testing.T) {
	// Simulate a reader that delivers partial content then errors.
	partialData := lines(
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "Partial"}}}`,
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": " content"}}}`,
	)

	reader := &errorAfterReader{
		data: []byte(partialData),
		err:  io.ErrUnexpectedEOF,
	}

	result, err := ParseClaudeCodeStream(reader, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream read error")
	require.NotNil(t, result)
	assert.True(t, result.Partial)
	assert.Equal(t, "Partial content", result.Content)
}

func TestParseClaudeCodeStream_EmptyStream(t *testing.T) {
	result, err := ParseClaudeCodeStream(strings.NewReader(""), nil)

	require.NoError(t, err)
	assert.Equal(t, "", result.Content)
	assert.False(t, result.Partial)
}

func TestParseClaudeCodeJSON_ResultObject(t *testing.T) {
	jsonStr := `{"result": "final text", "session_id": "abc-123", "usage": {"inputTokens": 100, "outputTokens": 50}}`

	result, err := ParseClaudeCodeJSON(strings.NewReader(jsonStr))

	require.NoError(t, err)
	assert.Equal(t, "final text", result.Content)
	assert.Equal(t, "abc-123", result.SessionID)
	assert.Equal(t, 100, result.Usage.PromptTokens)
	assert.Equal(t, 50, result.Usage.CompletionTokens)
	assert.Equal(t, 150, result.Usage.TotalTokens)
}

func TestParseClaudeCodeJSON_SnakeCaseUsage(t *testing.T) {
	jsonStr := `{"result": "hello", "session_id": "s-1", "usage": {"input_tokens": 200, "output_tokens": 80}}`

	result, err := ParseClaudeCodeJSON(strings.NewReader(jsonStr))

	require.NoError(t, err)
	assert.Equal(t, "hello", result.Content)
	assert.Equal(t, 200, result.Usage.PromptTokens)
	assert.Equal(t, 80, result.Usage.CompletionTokens)
	assert.Equal(t, 280, result.Usage.TotalTokens)
}

func TestParseClaudeCodeJSON_EmptyInput(t *testing.T) {
	result, err := ParseClaudeCodeJSON(strings.NewReader(""))

	require.NoError(t, err)
	assert.Equal(t, "", result.Content)
}

func TestParseClaudeCodeStream_MixedEvents(t *testing.T) {
	stream := lines(
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "Start. "}}}`,
		`{"type": "message", "content": [{"type": "tool_use", "name": "Bash", "input": {"command": "ls"}}]}`,
		`{"type": "system/api_retry", "attempt": 1, "max_retries": 3, "retry_delay_ms": 500}`,
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "End."}}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	// Only text_delta events contribute to content
	assert.Equal(t, "Start. End.", result.Content)
}

func TestParseClaudeCodeStream_UsageFromMessage(t *testing.T) {
	stream := lines(
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "Hi"}}}`,
		`{"type": "message", "role": "assistant", "content": [{"type": "text", "text": "Hi"}], "usage": {"input_tokens": 100, "output_tokens": 50}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "Hi", result.Content)
	assert.Equal(t, 100, result.Usage.PromptTokens)
	assert.Equal(t, 50, result.Usage.CompletionTokens)
	assert.Equal(t, 150, result.Usage.TotalTokens)
}

func TestParseClaudeCodeStream_UsageCamelCaseFromMessage(t *testing.T) {
	stream := lines(
		`{"type": "message", "role": "assistant", "content": [{"type": "text", "text": "Done"}], "usage": {"inputTokens": 75, "outputTokens": 25}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "Done", result.Content)
	assert.Equal(t, 75, result.Usage.PromptTokens)
	assert.Equal(t, 25, result.Usage.CompletionTokens)
	assert.Equal(t, 100, result.Usage.TotalTokens)
}

func TestParseClaudeCodeStream_SessionID(t *testing.T) {
	stream := lines(
		`{"type": "message", "role": "assistant", "content": [{"type": "text", "text": "OK"}], "session_id": "ses-xyz-789"}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "ses-xyz-789", result.SessionID)
}

func TestParseClaudeCodeStream_CallbackDoneSignal(t *testing.T) {
	stream := lines(
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "Hi"}}}`,
	)

	var mu sync.Mutex
	var gotDone bool
	var doneDeltas int

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), func(delta string, done bool) {
		mu.Lock()
		defer mu.Unlock()
		if done {
			gotDone = true
		} else {
			doneDeltas++
		}
	})

	require.NoError(t, err)
	assert.Equal(t, "Hi", result.Content)

	mu.Lock()
	assert.True(t, gotDone, "onDelta should have been called with done=true")
	assert.Equal(t, 1, doneDeltas)
	mu.Unlock()
}

func TestParseClaudeCodeStream_UnknownEventTypes(t *testing.T) {
	stream := lines(
		`{"type": "unknown_event", "data": "something"}`,
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "OK"}}}`,
		`{"type": "future_event_v2", "payload": {}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "OK", result.Content)
}

func TestParseClaudeCodeStream_FinalMessageTextWhenNoStreaming(t *testing.T) {
	// When no text_delta events precede the final message, the message's text
	// content should be used.
	stream := lines(
		`{"type": "message", "role": "assistant", "content": [{"type": "text", "text": "Direct response"}], "usage": {"input_tokens": 10, "output_tokens": 5}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "Direct response", result.Content)
}

func TestParseClaudeCodeStream_MalformedLines(t *testing.T) {
	stream := lines(
		`not json at all`,
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "OK"}}}`,
		`{broken json`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "OK", result.Content)
}

func TestParseClaudeCodeStream_NilCallback(t *testing.T) {
	stream := lines(
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "Hello"}}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "Hello", result.Content)
}

func TestParseClaudeCodeStream_JSONOutputFallback(t *testing.T) {
	// A line with no "type" field but a "result" field should be parsed as a
	// JSON output mode result object. This was previously broken by a duplicate
	// JSON struct tag that caused env.Result to always be nil.
	stream := lines(
		`{"result": "hello from json mode", "session_id": "s-json-1", "usage": {"inputTokens": 10, "outputTokens": 5}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "hello from json mode", result.Content)
	assert.Equal(t, "s-json-1", result.SessionID)
	assert.Equal(t, 10, result.Usage.PromptTokens)
	assert.Equal(t, 5, result.Usage.CompletionTokens)
}

func TestParseClaudeCodeStream_VerboseResultEvent(t *testing.T) {
	stream := lines(
		`{"type": "stream_event", "event": {"delta": {"type": "text_delta", "text": "Hi"}}}`,
		`{"type": "result", "result": "Hi", "session_id": "ses-result-1", "is_error": false, "usage": {"input_tokens": 200, "output_tokens": 80}}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "Hi", result.Content)
	assert.Equal(t, "ses-result-1", result.SessionID)
	assert.Equal(t, 200, result.Usage.PromptTokens)
	assert.Equal(t, 80, result.Usage.CompletionTokens)
}

func TestParseClaudeCodeStream_MultipleToolUseBlocks(t *testing.T) {
	stream := lines(
		`{"type": "message", "content": [{"type": "tool_use", "name": "Read", "input": {"file": "a.go"}}, {"type": "tool_use", "name": "Glob", "input": {"pattern": "*.go"}}]}`,
		`{"type": "message", "role": "assistant", "content": [{"type": "text", "text": "Found files."}]}`,
	)

	result, err := ParseClaudeCodeStream(strings.NewReader(stream), nil)

	require.NoError(t, err)
	assert.Equal(t, "Found files.", result.Content)
}

// --- helpers ---

// lines joins strings with newlines to simulate newline-delimited JSON.
func lines(ss ...string) string {
	return strings.Join(ss, "\n") + "\n"
}

// errorAfterReader delivers all data first, then returns an error on the next read.
type errorAfterReader struct {
	data []byte
	pos  int
	err  error
	done bool
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		if !r.done {
			r.done = true
			return 0, r.err
		}
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
