package models

import (
	"bytes"
	"encoding/json"
)

// Typed Anthropic Messages API wire format (conduit-31jg.36).
//
// These are the request/response/SSE types the ai.AnthropicProvider sends
// and parses. Request types marshal to exactly the JSON the API expects
// (field presence matters: e.g. a tool_result always carries "content", a
// tool_use always carries "input"). Response types decode leniently: a field
// of the wrong JSON kind is treated as absent instead of failing the whole
// response, and unknown content block / event / delta types decode to just
// their Type so callers can ignore them (server tool blocks, thinking, ...).
// That mirrors the comma-ok parsing the provider has relied on since
// conduit-31jg.12.

// ---------------------------------------------------------------------------
// Request
// ---------------------------------------------------------------------------

// Content block types used by this gateway.
const (
	BlockText       = "text"
	BlockImage      = "image"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

// MessagesRequest is the POST /v1/messages body.
type MessagesRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    *SystemPrompt   `json:"system,omitempty"`
	Messages  []Message       `json:"messages"`
	Tools     []AnthropicTool `json:"tools,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
}

// CacheControl is a prompt-caching breakpoint marker.
type CacheControl struct {
	Type string `json:"type"`          // "ephemeral"
	TTL  string `json:"ttl,omitempty"` // "1h" for the extended TTL; empty = default 5m
}

// ImageSource is the source of an image content block.
type ImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ContentBlock is one request content block — a tagged union on Type. Only
// the fields of the block's type are serialized (see MarshalJSON):
//
//	text:        text
//	image:       source
//	tool_use:    id, name, input (nil input is sent as {})
//	tool_result: tool_use_id, content, is_error (only when true)
//
// cache_control is added to any type when set.
type ContentBlock struct {
	Type string

	Text string // text

	Source *ImageSource // image

	ID    string                 // tool_use
	Name  string                 // tool_use
	Input map[string]interface{} // tool_use

	ToolUseID string // tool_result
	Content   string // tool_result
	IsError   bool   // tool_result

	CacheControl *CacheControl
}

// TextBlock returns a text content block.
func TextBlock(text string) ContentBlock { return ContentBlock{Type: BlockText, Text: text} }

// MarshalJSON emits exactly the wire fields for the block's type.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case BlockText:
		return json.Marshal(struct {
			Type         string        `json:"type"`
			Text         string        `json:"text"`
			CacheControl *CacheControl `json:"cache_control,omitempty"`
		}{b.Type, b.Text, b.CacheControl})
	case BlockImage:
		return json.Marshal(struct {
			Type         string        `json:"type"`
			Source       *ImageSource  `json:"source"`
			CacheControl *CacheControl `json:"cache_control,omitempty"`
		}{b.Type, b.Source, b.CacheControl})
	case BlockToolUse:
		input := b.Input
		if input == nil {
			input = map[string]interface{}{} // the API requires an object
		}
		return json.Marshal(struct {
			Type         string                 `json:"type"`
			ID           string                 `json:"id"`
			Name         string                 `json:"name"`
			Input        map[string]interface{} `json:"input"`
			CacheControl *CacheControl          `json:"cache_control,omitempty"`
		}{b.Type, b.ID, b.Name, input, b.CacheControl})
	case BlockToolResult:
		return json.Marshal(struct {
			Type         string        `json:"type"`
			ToolUseID    string        `json:"tool_use_id"`
			Content      string        `json:"content"`
			IsError      bool          `json:"is_error,omitempty"`
			CacheControl *CacheControl `json:"cache_control,omitempty"`
		}{b.Type, b.ToolUseID, b.Content, b.IsError, b.CacheControl})
	default:
		return json.Marshal(struct {
			Type         string        `json:"type"`
			CacheControl *CacheControl `json:"cache_control,omitempty"`
		}{b.Type, b.CacheControl})
	}
}

// Message is one conversation turn. Content is a plain string (Text) unless
// Blocks is non-nil, in which case it is a content block array.
type Message struct {
	Role   string
	Text   string
	Blocks []ContentBlock
}

// HasBlocks reports whether the message is in content-block form.
func (m Message) HasBlocks() bool { return m.Blocks != nil }

// MarshalJSON emits {"role":..., "content": string | [blocks]}.
func (m Message) MarshalJSON() ([]byte, error) {
	if m.Blocks != nil {
		return json.Marshal(struct {
			Role    string         `json:"role"`
			Content []ContentBlock `json:"content"`
		}{m.Role, m.Blocks})
	}
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{m.Role, m.Text})
}

// SystemPrompt is the top-level "system" field: a plain string (Text) unless
// Blocks is non-nil. OAuth requests and any prompt carrying cache_control
// must use the block form.
type SystemPrompt struct {
	Text   string
	Blocks []ContentBlock
}

// MarshalJSON emits a string or a block array.
func (s SystemPrompt) MarshalJSON() ([]byte, error) {
	if s.Blocks != nil {
		return json.Marshal(s.Blocks)
	}
	return json.Marshal(s.Text)
}

// ---------------------------------------------------------------------------
// Response (non-streaming) and shared pieces
// ---------------------------------------------------------------------------

// MessagesResponse is a non-streaming /v1/messages reply (also the
// "message" of an SSE message_start event).
type MessagesResponse struct {
	ID         string
	Type       string
	Role       string
	Model      string
	Content    []ResponseBlock // nil when "content" is absent or not an array
	StopReason string          // "" when absent or not a string
	Usage      *Usage          // nil when "usage" is absent or not an object
}

// UnmarshalJSON decodes leniently. It fails only when the payload is not a
// JSON object (or null) — the same cases a decode into
// map[string]interface{} fails.
func (r *MessagesResponse) UnmarshalJSON(b []byte) error {
	f, err := decodeFields(b)
	if err != nil {
		return err
	}
	*r = MessagesResponse{}
	r.ID, _ = f.str("id")
	r.Type, _ = f.str("type")
	r.Role, _ = f.str("role")
	r.Model, _ = f.str("model")
	r.StopReason, _ = f.str("stop_reason")
	if raw, ok := f["content"]; ok && isKind(raw, '[') {
		var blocks []ResponseBlock
		if json.Unmarshal(raw, &blocks) == nil {
			r.Content = blocks
		}
	}
	r.Usage = f.usage("usage")
	return nil
}

// ResponseBlock is one content block of a reply or an SSE
// content_block_start. Only the fields this gateway uses are kept; any
// other block type (thinking, server_tool_use, web_search_tool_result, ...)
// decodes to its Type alone. A non-object element decodes to the zero
// block (Type "").
type ResponseBlock struct {
	Type  string
	Text  string
	ID    string
	Name  string
	Input map[string]interface{} // nil unless "input" is a JSON object

	hasText, hasID, hasName bool
}

// HasText reports whether "text" was present as a string.
func (b ResponseBlock) HasText() bool { return b.hasText }

// HasID reports whether "id" was present as a string.
func (b ResponseBlock) HasID() bool { return b.hasID }

// HasName reports whether "name" was present as a string.
func (b ResponseBlock) HasName() bool { return b.hasName }

// UnmarshalJSON never fails; see ResponseBlock.
func (b *ResponseBlock) UnmarshalJSON(data []byte) error {
	*b = ResponseBlock{}
	f, err := decodeFields(data)
	if err != nil || f == nil {
		return nil
	}
	b.Type, _ = f.str("type")
	b.Text, b.hasText = f.str("text")
	b.ID, b.hasID = f.str("id")
	b.Name, b.hasName = f.str("name")
	if raw, ok := f["input"]; ok && isKind(raw, '{') {
		var in map[string]interface{}
		if json.Unmarshal(raw, &in) == nil {
			b.Input = in
		}
	}
	return nil
}

// Usage is the token usage object. Absent or non-numeric counters are 0;
// fractional values are truncated.
type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// UnmarshalJSON never fails; see Usage.
func (u *Usage) UnmarshalJSON(data []byte) error {
	*u = Usage{}
	f, err := decodeFields(data)
	if err != nil || f == nil {
		return nil
	}
	u.InputTokens = f.num("input_tokens")
	u.OutputTokens = f.num("output_tokens")
	u.CacheCreationInputTokens = f.num("cache_creation_input_tokens")
	u.CacheReadInputTokens = f.num("cache_read_input_tokens")
	return nil
}

// APIError is the "error" object of an error reply or SSE error event.
type APIError struct {
	Type    string // "" when absent or not a string
	Message string // "" when absent or not a string
}

// UnmarshalJSON never fails; see APIError.
func (e *APIError) UnmarshalJSON(data []byte) error {
	*e = APIError{}
	f, err := decodeFields(data)
	if err != nil || f == nil {
		return nil
	}
	e.Type, _ = f.str("type")
	e.Message, _ = f.str("message")
	return nil
}

// ---------------------------------------------------------------------------
// Streaming (SSE)
// ---------------------------------------------------------------------------

// SSE event types.
const (
	EventMessageStart      = "message_start"
	EventContentBlockStart = "content_block_start"
	EventContentBlockDelta = "content_block_delta"
	EventContentBlockStop  = "content_block_stop"
	EventMessageDelta      = "message_delta"
	EventMessageStop       = "message_stop"
	EventPing              = "ping"
	EventError             = "error"
)

// content_block_delta delta types.
const (
	DeltaText      = "text_delta"
	DeltaInputJSON = "input_json_delta"
)

// StreamEvent is the data payload of one SSE event. Each nested field is
// nil unless present as a JSON object.
type StreamEvent struct {
	Type         string
	Message      *MessagesResponse // message_start
	ContentBlock *ResponseBlock    // content_block_start
	Delta        *StreamDelta      // content_block_delta, message_delta
	Usage        *Usage            // message_delta
	Error        *APIError         // error

	// RawContentBlock is the raw content_block value (for diagnostics when
	// it is malformed).
	RawContentBlock json.RawMessage
}

// UnmarshalJSON fails only when the payload is not a JSON object (or null).
func (e *StreamEvent) UnmarshalJSON(data []byte) error {
	f, err := decodeFields(data)
	if err != nil {
		return err
	}
	*e = StreamEvent{}
	e.Type, _ = f.str("type")
	if raw, ok := f["message"]; ok && isKind(raw, '{') {
		var m MessagesResponse
		if json.Unmarshal(raw, &m) == nil {
			e.Message = &m
		}
	}
	if raw, ok := f["content_block"]; ok {
		e.RawContentBlock = raw
		if isKind(raw, '{') {
			var cb ResponseBlock
			_ = json.Unmarshal(raw, &cb)
			e.ContentBlock = &cb
		}
	}
	if raw, ok := f["delta"]; ok && isKind(raw, '{') {
		var d StreamDelta
		_ = json.Unmarshal(raw, &d)
		e.Delta = &d
	}
	e.Usage = f.usage("usage")
	if raw, ok := f["error"]; ok && isKind(raw, '{') {
		var ae APIError
		_ = json.Unmarshal(raw, &ae)
		e.Error = &ae
	}
	return nil
}

// StreamDelta is the "delta" of content_block_delta (text_delta,
// input_json_delta, ...) or of message_delta (stop_reason).
type StreamDelta struct {
	Type        string
	Text        string
	PartialJSON string
	StopReason  string

	hasText, hasPartialJSON, hasStopReason bool
}

// HasText reports whether "text" was present as a string.
func (d StreamDelta) HasText() bool { return d.hasText }

// HasPartialJSON reports whether "partial_json" was present as a string.
func (d StreamDelta) HasPartialJSON() bool { return d.hasPartialJSON }

// HasStopReason reports whether "stop_reason" was present as a string
// (null, as sent on non-final message_delta events, is absent).
func (d StreamDelta) HasStopReason() bool { return d.hasStopReason }

// UnmarshalJSON never fails; see StreamDelta.
func (d *StreamDelta) UnmarshalJSON(data []byte) error {
	*d = StreamDelta{}
	f, err := decodeFields(data)
	if err != nil || f == nil {
		return nil
	}
	d.Type, _ = f.str("type")
	d.Text, d.hasText = f.str("text")
	d.PartialJSON, d.hasPartialJSON = f.str("partial_json")
	d.StopReason, d.hasStopReason = f.str("stop_reason")
	return nil
}

// ---------------------------------------------------------------------------
// Lenient field decoding
// ---------------------------------------------------------------------------

type fields map[string]json.RawMessage

// decodeFields decodes a JSON object into its raw fields. null yields a nil
// map; any other non-object is an error.
func decodeFields(b []byte) (fields, error) {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(b, &f); err != nil {
		// Report the error as a decode into a generic JSON object, the
		// form callers logged before these types existed (e.g. "cannot
		// unmarshal array into Go value of type map[string]interface {}").
		var generic map[string]interface{}
		if gerr := json.Unmarshal(b, &generic); gerr != nil {
			return nil, gerr
		}
		return nil, err
	}
	return f, nil
}

// str returns the field as a string and whether it was a JSON string.
func (f fields) str(key string) (string, bool) {
	raw, ok := f[key]
	if !ok || !isKind(raw, '"') {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// num returns a numeric field truncated to int, or 0 when absent or not a
// JSON number.
func (f fields) num(key string) int {
	raw, ok := f[key]
	if !ok {
		return 0
	}
	var v float64
	if json.Unmarshal(raw, &v) != nil {
		return 0
	}
	return int(v)
}

// usage returns the field as a Usage when it is a JSON object, else nil.
func (f fields) usage(key string) *Usage {
	raw, ok := f[key]
	if !ok || !isKind(raw, '{') {
		return nil
	}
	var u Usage
	_ = json.Unmarshal(raw, &u)
	return &u
}

// isKind reports whether raw JSON starts with the given delimiter
// ('{' object, '[' array, '"' string).
func isKind(raw json.RawMessage, first byte) bool {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	return len(raw) > 0 && raw[0] == first
}

// AnthropicTool represents a tool definition for the Anthropic API.
// conduit-31jg.36: also the tool type of MessagesRequest; CacheControl marks
// the tools cache breakpoint.
type AnthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  map[string]interface{} `json:"input_schema"`
	CacheControl *CacheControl          `json:"cache_control,omitempty"`
}
