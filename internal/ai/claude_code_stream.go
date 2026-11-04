package ai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
)

// ClaudeCodeStreamResult holds the parsed result from a claude -p stream.
type ClaudeCodeStreamResult struct {
	Content   string // Accumulated text response
	SessionID string // Claude Code's session ID for --resume
	Usage     Usage  // Token usage stats
	Partial   bool   // True if stream was interrupted
}

// streamEvent represents the top-level envelope for stream-json events.
type streamEvent struct {
	Type  string          `json:"type"`
	Event json.RawMessage `json:"event,omitempty"`

	// Fields for "message" type events
	Role     string          `json:"role,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
	UsageRaw json.RawMessage `json:"usage,omitempty"`

	// Fields for "system/api_retry" events
	Attempt    int `json:"attempt,omitempty"`
	MaxRetries int `json:"max_retries,omitempty"`
	RetryDelay int `json:"retry_delay_ms,omitempty"`

	// Fields for "system" init events
	SubType string `json:"subtype,omitempty"`

	// Fields for "assistant" events (verbose mode) — message is nested
	Message json.RawMessage `json:"message,omitempty"`

	// Fields for "result" events (verbose mode) and JSON output mode (no type field)
	Result    *string `json:"result,omitempty"`
	IsError   bool    `json:"is_error,omitempty"`
	SessionID string  `json:"session_id,omitempty"`

	// ParentToolUseID is set on events from a sub-agent (Task tool) run
	// inside the main agent's turn; null/absent for the main agent.
	ParentToolUseID *string `json:"parent_tool_use_id,omitempty"`
}

// fromSubagent reports whether the event belongs to a sub-agent run.
func (e *streamEvent) fromSubagent() bool {
	return e.ParentToolUseID != nil && *e.ParentToolUseID != ""
}

// streamDelta represents the inner delta object within a stream_event.
type streamDelta struct {
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// contentBlock represents a content block in a message.
type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// claudeCodeUsage represents usage stats from Claude Code, supporting both
// camelCase (JSON output) and snake_case (stream events) field names.
type claudeCodeUsage struct {
	// camelCase (JSON output mode)
	InputTokensCC   int `json:"inputTokens,omitempty"`
	OutputTokensCC  int `json:"outputTokens,omitempty"`
	CacheReadCC     int `json:"cacheReadInputTokens,omitempty"`
	CacheCreationCC int `json:"cacheCreationInputTokens,omitempty"`
	// snake_case (stream message events)
	InputTokensSC   int `json:"input_tokens,omitempty"`
	OutputTokensSC  int `json:"output_tokens,omitempty"`
	CacheReadSC     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationSC int `json:"cache_creation_input_tokens,omitempty"`
}

// resultUsage represents the usage field inside the verbose "result" event,
// which uses snake_case at the top level.
type resultUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// toUsage converts to the canonical Usage type, preferring whichever naming
// convention has a non-zero value.
func (u *claudeCodeUsage) toUsage() Usage {
	input := u.InputTokensCC
	if input == 0 {
		input = u.InputTokensSC
	}
	output := u.OutputTokensCC
	if output == 0 {
		output = u.OutputTokensSC
	}
	cacheRead := u.CacheReadCC
	if cacheRead == 0 {
		cacheRead = u.CacheReadSC
	}
	cacheCreation := u.CacheCreationCC
	if cacheCreation == 0 {
		cacheCreation = u.CacheCreationSC
	}
	return Usage{
		PromptTokens:             input,
		CompletionTokens:         output,
		TotalTokens:              input + output,
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: cacheCreation,
	}
}

// assistantMessage represents the nested message inside an "assistant" event.
type assistantMessage struct {
	ID        string          `json:"id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	UsageRaw  json.RawMessage `json:"usage,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
}

// verboseResult represents the full "result" event from verbose stream-json.
type verboseResult struct {
	Result     string          `json:"result"`
	IsError    bool            `json:"is_error"`
	SessionID  string          `json:"session_id"`
	Usage      json.RawMessage `json:"usage,omitempty"`
	ModelUsage json.RawMessage `json:"model_usage,omitempty"`
}

// modelUsageEntry represents per-model usage stats inside a verbose result's model_usage field.
type modelUsageEntry struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
}

// ParseClaudeCodeStream reads newline-delimited JSON from r (the stdout of
// `claude -p --output-format stream-json`), calls onDelta for each text chunk,
// and returns the accumulated result. Unknown event types are silently skipped.
//
// Supported event types (verbose stream-json format):
//   - "system" — init event; session_id extracted
//   - "assistant" — message content blocks; text and tool_use extracted
//   - "stream_event" — inner delta for text_delta chunks
//   - "message" — final message with content blocks and usage
//   - "result" — final result with text, session_id, and usage
//   - "system/api_retry" — retry notification
//
// The returned Content is the run's final answer (H7): a multi-step run
// emits interim assistant text ("Let me check the files.") before each tool
// call, and only the last turn's text is the reply. It is, in order of
// preference: the "result" event's text; the text of the assistant
// message(s) after the last tool result; the text deltas streamed after the
// last tool result. Every main-agent text delta is passed to onDelta once
// (the complete assistant events that repeat it are not re-streamed);
// sub-agent events (parent_tool_use_id set) are ignored.
//
// If the reader closes unexpectedly, the result has Partial set to true with
// whatever content was accumulated up to that point.
func ParseClaudeCodeStream(r io.Reader, onDelta StreamCallback) (*ClaudeCodeStreamResult, error) {
	scanner := bufio.NewScanner(r)
	// Allow lines up to 1 MB (tool_use input can be large).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// The current segment is everything since the last tool result: the
	// final segment holds the answer.
	var segText strings.Builder   // assistant/message text blocks
	var segDeltas strings.Builder // text_delta chunks
	segSeen := map[string]bool{}  // message id + text already in segText
	addSegText := func(msgID, text string) {
		if text == "" || segSeen[msgID+"\x00"+text] {
			return // cumulative re-emission of the same block
		}
		segSeen[msgID+"\x00"+text] = true
		segText.WriteString(text)
	}
	var resultText string
	var sessionID string
	var usage Usage
	var scanErr error

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var env streamEvent
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			log.Printf("[ClaudeCodeStream] Skipping unparseable line: %v", err)
			continue
		}

		if env.fromSubagent() {
			continue // a sub-agent's turn is not the reply
		}

		switch env.Type {
		case "system":
			// Init event — grab session_id for --resume support
			if env.SessionID != "" {
				sessionID = env.SessionID
			}

		case "assistant":
			// Verbose mode: assistant message with nested message.content
			if env.Message != nil {
				var msg assistantMessage
				if err := json.Unmarshal(env.Message, &msg); err == nil {
					// Grab session_id from message
					if msg.SessionID != "" {
						sessionID = msg.SessionID
					}
					// Parse content blocks
					if msg.Content != nil {
						var blocks []contentBlock
						if err := json.Unmarshal(msg.Content, &blocks); err == nil {
							for _, b := range blocks {
								switch b.Type {
								case "text":
									// Repeats any deltas already streamed for
									// this message; recorded, not re-streamed.
									addSegText(msg.ID, b.Text)
								case "tool_use":
									log.Printf("[ClaudeCodeStream] Tool use: %s", b.Name)
								}
							}
						}
					}
					// Parse usage
					if msg.UsageRaw != nil {
						var cu claudeCodeUsage
						if err := json.Unmarshal(msg.UsageRaw, &cu); err == nil {
							usage = cu.toUsage()
						}
					}
				}
			}

		case "result":
			// Verbose mode: final result event with accumulated text, usage, session_id
			var vr verboseResult
			if err := json.Unmarshal([]byte(line), &vr); err == nil {
				// The result text is the run's final answer.
				if vr.Result != "" {
					resultText = vr.Result
				}
				if vr.SessionID != "" {
					sessionID = vr.SessionID
				}
				// Parse usage from result event
				if vr.Usage != nil {
					var ru resultUsage
					if err := json.Unmarshal(vr.Usage, &ru); err == nil {
						if ru.InputTokens > 0 || ru.OutputTokens > 0 {
							usage = Usage{
								PromptTokens:     ru.InputTokens,
								CompletionTokens: ru.OutputTokens,
								TotalTokens:      ru.InputTokens + ru.OutputTokens,
							}
						}
					}
				}
				// Parse per-model usage for cache token stats
				if vr.ModelUsage != nil {
					var mu map[string]modelUsageEntry
					if err := json.Unmarshal(vr.ModelUsage, &mu); err == nil {
						for _, entry := range mu {
							usage.CacheReadInputTokens += entry.CacheReadInputTokens
							usage.CacheCreationInputTokens += entry.CacheCreationInputTokens
						}
					}
				}
			}

		case "stream_event":
			// Parse inner event for text_delta
			if env.Event != nil {
				var sd streamDelta
				if err := json.Unmarshal(env.Event, &sd); err == nil && sd.Delta.Type == "text_delta" {
					segDeltas.WriteString(sd.Delta.Text)
					if onDelta != nil {
						onDelta(sd.Delta.Text, false)
					}
				}
			}

		case "message":
			// Parse content blocks
			if env.Content != nil {
				var blocks []contentBlock
				if err := json.Unmarshal(env.Content, &blocks); err == nil {
					for _, b := range blocks {
						switch b.Type {
						case "text":
							// Complete message text; repeats any streamed deltas.
							addSegText("", b.Text)
						case "tool_use":
							log.Printf("[ClaudeCodeStream] Tool use: %s", b.Name)
						}
					}
				}
			}

			// Parse usage from message
			if env.UsageRaw != nil {
				var cu claudeCodeUsage
				if err := json.Unmarshal(env.UsageRaw, &cu); err == nil {
					usage = cu.toUsage()
				}
			}

			// Capture session_id if present
			if env.SessionID != "" {
				sessionID = env.SessionID
			}

		case "system/api_retry":
			log.Printf("[ClaudeCodeStream] API retry: attempt %d/%d (delay %dms)",
				env.Attempt, env.MaxRetries, env.RetryDelay)

		case "rate_limit_event":
			// Rate limit status notification — informational, skip

		case "user":
			// Claude Code echoes tool results as "user" events: the next
			// assistant turn starts a new segment, so interim text before a
			// tool call is never returned as the answer.
			segText.Reset()
			segDeltas.Reset()
			clear(segSeen)

		default:
			// Check for JSON output mode result object (no "type" field, has "result")
			if env.Type == "" && env.Result != nil {
				return parseResultObject([]byte(line))
			}
			// Unknown event type -- skip gracefully
			if env.Type != "" {
				log.Printf("[ClaudeCodeStream] Skipping unknown event type: %s", env.Type)
			}
		}
	}

	scanErr = scanner.Err()

	content := resultText
	if content == "" {
		content = segText.String()
	}
	if content == "" {
		content = segDeltas.String()
	}

	result := &ClaudeCodeStreamResult{
		Content:   content,
		SessionID: sessionID,
		Usage:     usage,
	}

	// Signal completion to callback
	if onDelta != nil {
		onDelta("", true)
	}

	if scanErr != nil {
		result.Partial = true
		return result, fmt.Errorf("stream read error: %w", scanErr)
	}

	return result, nil
}

// parseResultObject parses a JSON output mode result (from --output-format json).
func parseResultObject(data []byte) (*ClaudeCodeStreamResult, error) {
	var raw struct {
		Result    string          `json:"result"`
		SessionID string          `json:"session_id"`
		Usage     json.RawMessage `json:"usage,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse result object: %w", err)
	}

	result := &ClaudeCodeStreamResult{
		Content:   raw.Result,
		SessionID: raw.SessionID,
	}

	if raw.Usage != nil {
		var cu claudeCodeUsage
		if err := json.Unmarshal(raw.Usage, &cu); err == nil {
			result.Usage = cu.toUsage()
		}
	}

	return result, nil
}

// ParseClaudeCodeJSON parses a single JSON response from `claude -p --output-format json`.
func ParseClaudeCodeJSON(r io.Reader) (*ClaudeCodeStreamResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON response: %w", err)
	}

	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return &ClaudeCodeStreamResult{}, nil
	}

	return parseResultObject(data)
}
