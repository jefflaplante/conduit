package ai

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H7: a multi-step `claude -p` run (text + tool_use, tool_result, final
// answer) must return the final answer, not the first interim text.

const (
	ccInit       = `{"type":"system","subtype":"init","session_id":"cc-sess-1","model":"claude-sonnet-4"}`
	ccInterimMsg = `{"type":"assistant","message":{"id":"msg_01","role":"assistant","content":[{"type":"text","text":"Let me check the files."},{"type":"tool_use","id":"tu_01","name":"Glob","input":{"pattern":"*.go"}}],"usage":{"input_tokens":100,"output_tokens":20}},"parent_tool_use_id":null,"session_id":"cc-sess-1"}`
	ccToolResult = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_01","content":"a.go\nb.go"}]},"parent_tool_use_id":null,"session_id":"cc-sess-1"}`
	ccFinalMsg   = `{"type":"assistant","message":{"id":"msg_02","role":"assistant","content":[{"type":"text","text":"There are two Go files: a.go and b.go."}],"usage":{"input_tokens":150,"output_tokens":12}},"parent_tool_use_id":null,"session_id":"cc-sess-1"}`
	ccResult     = `{"type":"result","subtype":"success","is_error":false,"result":"There are two Go files: a.go and b.go.","session_id":"cc-sess-1","usage":{"input_tokens":250,"output_tokens":32}}`
	ccFinalText  = "There are two Go files: a.go and b.go."
)

// ccDelta is a --include-partial-messages text delta.
func ccDelta(text string) string {
	return `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}},"parent_tool_use_id":null,"session_id":"cc-sess-1"}`
}

func collectDeltas(t *testing.T, stream string) (*ClaudeCodeStreamResult, []string) {
	t.Helper()
	var deltas []string
	res, err := ParseClaudeCodeStream(strings.NewReader(stream), func(d string, done bool) {
		if !done {
			deltas = append(deltas, d)
		}
	})
	require.NoError(t, err)
	return res, deltas
}

func TestParseClaudeCodeStream_MultiStepReturnsFinalAnswer(t *testing.T) {
	// Without partial messages: system, assistant(text+tool_use),
	// user(tool_result), assistant(final), result.
	res, deltas := collectDeltas(t, lines(ccInit, ccInterimMsg, ccToolResult, ccFinalMsg, ccResult))
	assert.Equal(t, ccFinalText, res.Content)
	assert.Equal(t, "cc-sess-1", res.SessionID)
	assert.Equal(t, 250, res.Usage.PromptTokens)
	assert.Empty(t, deltas)
}

func TestParseClaudeCodeStream_MultiStepWithoutResultUsesLastAssistant(t *testing.T) {
	res, _ := collectDeltas(t, lines(ccInit, ccInterimMsg, ccToolResult, ccFinalMsg))
	assert.Equal(t, ccFinalText, res.Content)
}

func TestParseClaudeCodeStream_ResultTextPreferred(t *testing.T) {
	// The result event is authoritative even when it differs from the
	// streamed/assistant text.
	result := `{"type":"result","subtype":"success","is_error":false,"result":"Authoritative answer.","session_id":"cc-sess-1"}`
	res, _ := collectDeltas(t, lines(ccInit, ccDelta("Streamed text."), ccFinalMsg, result))
	assert.Equal(t, "Authoritative answer.", res.Content)
}

func TestParseClaudeCodeStream_MultiStepPartialMessages(t *testing.T) {
	// With --include-partial-messages: every text chunk arrives once as a
	// stream_event delta and again inside the complete assistant event.
	stream := lines(
		ccInit,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_01"}},"parent_tool_use_id":null,"session_id":"cc-sess-1"}`,
		ccDelta("Let me check "),
		ccDelta("the files."),
		ccInterimMsg,
		ccToolResult,
		ccDelta("There are two Go files: "),
		ccDelta("a.go and b.go."),
		ccFinalMsg,
		ccResult,
	)
	res, deltas := collectDeltas(t, stream)
	assert.Equal(t, ccFinalText, res.Content)
	// Each delta is streamed exactly once; assistant events add none.
	assert.Equal(t, []string{"Let me check ", "the files.", "There are two Go files: ", "a.go and b.go."}, deltas)
}

func TestParseClaudeCodeStream_PartialMessagesNoResultUsesLastSegment(t *testing.T) {
	// Stream ends during the final turn (no assistant/result event for it):
	// the reply is the final turn's deltas, not interim + final.
	stream := lines(ccInit, ccDelta("Let me check the files."), ccInterimMsg, ccToolResult, ccDelta("Partial final"))
	res, _ := collectDeltas(t, stream)
	assert.Equal(t, "Partial final", res.Content)
}

func TestParseClaudeCodeStream_InterimTextNeverReturnedAsAnswer(t *testing.T) {
	// The run stopped after a tool result with no final text and no result
	// text: the interim "Let me check" is not the answer.
	res, _ := collectDeltas(t, lines(ccInit, ccInterimMsg, ccToolResult))
	assert.Empty(t, res.Content)
}

func TestParseClaudeCodeStream_SingleStep(t *testing.T) {
	single := `{"type":"assistant","message":{"id":"msg_01","role":"assistant","content":[{"type":"text","text":"Hello there."}]},"parent_tool_use_id":null,"session_id":"cc-sess-1"}`
	singleResult := `{"type":"result","subtype":"success","is_error":false,"result":"Hello there.","session_id":"cc-sess-1"}`

	res, _ := collectDeltas(t, lines(ccInit, single, singleResult))
	assert.Equal(t, "Hello there.", res.Content)

	res, _ = collectDeltas(t, lines(ccInit, single))
	assert.Equal(t, "Hello there.", res.Content)

	res, deltas := collectDeltas(t, lines(ccInit, ccDelta("Hello "), ccDelta("there."), single, singleResult))
	assert.Equal(t, "Hello there.", res.Content)
	assert.Equal(t, []string{"Hello ", "there."}, deltas)
}

func TestParseClaudeCodeStream_PerBlockAssistantEvents(t *testing.T) {
	// The CLI may emit one assistant event per content block of the same
	// message; the final message's text blocks are joined, not dropped.
	a := `{"type":"assistant","message":{"id":"msg_02","role":"assistant","content":[{"type":"text","text":"Part one. "}]},"parent_tool_use_id":null}`
	b := `{"type":"assistant","message":{"id":"msg_02","role":"assistant","content":[{"type":"text","text":"Part two."}]},"parent_tool_use_id":null}`
	res, _ := collectDeltas(t, lines(ccInit, ccInterimMsg, ccToolResult, a, b))
	assert.Equal(t, "Part one. Part two.", res.Content)
}

func TestParseClaudeCodeStream_SubagentEventsIgnored(t *testing.T) {
	// Events from a sub-agent (Task tool) carry parent_tool_use_id; they are
	// neither the answer nor streamed to the user.
	subDelta := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"sub-agent chatter"}},"parent_tool_use_id":"tu_01","session_id":"cc-sess-1"}`
	subMsg := `{"type":"assistant","message":{"id":"msg_sub","role":"assistant","content":[{"type":"text","text":"sub-agent result"}]},"parent_tool_use_id":"tu_01","session_id":"cc-sess-1"}`
	res, deltas := collectDeltas(t, lines(ccInit, ccInterimMsg, subDelta, subMsg, ccToolResult, ccFinalMsg))
	assert.Equal(t, ccFinalText, res.Content)
	assert.Empty(t, deltas)
}

func TestClaudeCodeProvider_BuildCommand_PartialMessagesFlag(t *testing.T) {
	p := &ClaudeCodeProvider{
		name:   "test",
		config: config.ClaudeCodeConfig{ClaudePath: "/usr/bin/claude", TimeoutSeconds: 60},
	}

	cmd, cleanup, err := p.buildCommand(context.Background(), "hi", "", "", true)
	require.NoError(t, err)
	defer cleanup()
	assert.Contains(t, cmd.Args, "--include-partial-messages", "streaming runs must request text deltas")

	cmd, cleanup2, err := p.buildCommand(context.Background(), "hi", "", "", false)
	require.NoError(t, err)
	defer cleanup2()
	assert.NotContains(t, cmd.Args, "--include-partial-messages", "flag is only valid with stream-json")
}
