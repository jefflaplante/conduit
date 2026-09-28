package ai

import (
	"context"

	"conduit/internal/sessions"
)

// AgentProcessedResponse represents processed response from agent (to avoid circular imports)
type AgentProcessedResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Silent    bool       `json:"silent,omitempty"`
	Modified  bool       `json:"modified,omitempty"`
}

// AgentSystem interface for dependency injection
type AgentSystem interface {
	BuildSystemPrompt(ctx context.Context, session *sessions.Session) ([]SystemBlock, error)
	GetToolDefinitions(session *sessions.Session) []Tool
	ProcessResponse(ctx context.Context, response *GenerateResponse) (*AgentProcessedResponse, error)
}

// SystemBlock represents a system prompt block
type SystemBlock struct {
	Type string      `json:"type"`
	Text string      `json:"text,omitempty"`
	Meta interface{} `json:"meta,omitempty"`
	// Dynamic marks per-turn content (timestamp, wake context). Providers
	// with prompt caching place the system breakpoint on the last
	// non-dynamic block so these never invalidate the cached prefix.
	// conduit-31jg.14
	Dynamic bool `json:"dynamic,omitempty"`
}

// ProcessedResponse represents processed AI response
type ProcessedResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Silent    bool       `json:"silent,omitempty"`
	Modified  bool       `json:"modified,omitempty"`
}

// Provider defines the interface for AI providers
type Provider interface {
	Name() string
	GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error)
}

// StreamingProvider is an optional extension of Provider for streaming support.
// Providers that implement this interface can deliver token-by-token responses.
type StreamingProvider interface {
	Provider
	GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error)
}

// GenerateRequest represents a request to generate an AI response
type GenerateRequest struct {
	Messages  []ChatMessage `json:"messages"`
	Model     string        `json:"model,omitempty"`
	Tools     []Tool        `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

// GenerateResponse represents an AI provider's response
type GenerateResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     Usage      `json:"usage,omitempty"`
	Partial   bool       `json:"partial,omitempty"` // True when response is incomplete due to mid-stream error
	// FinishReason is the provider-reported stop condition for the generation:
	// "stop" (natural end), "length" (hit max_tokens mid-generation — the
	// content is truncated, NOT a complete answer), "tool_calls" (model wants
	// tools), "content_filter". Empty when the provider did not report one.
	// bd-1k3o: parsed from OpenAI-compatible responses so the tool loop can
	// detect length-truncated finals instead of silently accepting them.
	FinishReason string `json:"finish_reason,omitempty"`
	// StopReason is the raw provider stop reason when it differs in
	// vocabulary from FinishReason (Anthropic stop_reason: "end_turn",
	// "max_tokens", "tool_use", "refusal", "pause_turn", ...). conduit-31jg.11.
	StopReason string `json:"stop_reason,omitempty"`
}

// ChatMessage represents a message in a conversation
type ChatMessage struct {
	Role        string       `json:"role"` // "system", "user", "assistant", "tool"
	Content     string       `json:"content"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`   // For assistant messages with tool calls
	ToolCallID  string       `json:"tool_call_id,omitempty"` // For tool result messages
	Attachments []Attachment `json:"attachments,omitempty"`  // In-memory media attachments (images, etc.)
	// IsError marks a tool result message whose tool call failed (Go error or
	// Result.Success=false). Anthropic receives it as tool_result.is_error.
	// conduit-31jg.45
	IsError bool `json:"is_error,omitempty"`
	// SystemBlocks carries the agent's system prompt blocks on the leading
	// system message. Content still holds them joined with "\n\n" for
	// providers without block support; Anthropic sends the blocks so the
	// cache breakpoint lands on the static one. conduit-31jg.14
	SystemBlocks []SystemBlock `json:"system_blocks,omitempty"`
	// Injected marks a user-role message the gateway synthesized mid-turn
	// (the length auto-continue "continue", tool-loop guidance) rather than
	// one the user typed. In-memory only; providers ignore it. Used so goal
	// extraction never mistakes one for the user's request (conduit-31jg.87).
	Injected bool `json:"-"`
}

// Attachment represents media content attached to a message (e.g., images from Telegram).
// Carried in-memory only for the current request; never persisted to the database.
type Attachment struct {
	Type      string // "image", "document", "audio"
	MediaType string // MIME type: "image/jpeg", "image/png", etc.
	Data      []byte // Raw bytes
}

// Tool represents a tool/function that the AI can call
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCall represents a tool function call from the AI
type ToolCall struct {
	ID   string                 `json:"id"`
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"arguments"`
}

// Usage represents token usage statistics
type Usage struct {
	PromptTokens             int `json:"prompt_tokens"`
	CompletionTokens         int `json:"completion_tokens"`
	TotalTokens              int `json:"total_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	// ContextTokens is the full prompt size of the LAST round trip folded in
	// by Add (see Usage.Context). The fields above are whole-turn sums.
	// conduit-31jg.15
	ContextTokens int `json:"context_tokens,omitempty"`
	// CostUSD is the priced cost of the provider calls folded into this
	// usage; PricedCalls/UnpricedCalls count them (an unpriced call — model
	// with no known price — contributes 0 to CostUSD). Set per call by the
	// router's metering hook and summed by Add. conduit-31jg.64
	CostUSD       float64 `json:"cost_usd,omitempty"`
	PricedCalls   int     `json:"priced_calls,omitempty"`
	UnpricedCalls int     `json:"unpriced_calls,omitempty"`
}

// SimpleConversationResponse implements ConversationResponse for non-tool responses
type SimpleConversationResponse struct {
	Content string `json:"content"`
	Usage   *Usage `json:"usage"`
	Steps   int    `json:"steps"`
}

func (s *SimpleConversationResponse) GetContent() string {
	return s.Content
}

func (s *SimpleConversationResponse) GetUsage() *Usage {
	return s.Usage
}

func (s *SimpleConversationResponse) GetSteps() int {
	return s.Steps
}

func (s *SimpleConversationResponse) HasToolResults() bool {
	return false
}

// contextKey is a private type for context keys in the ai package.
type contextKey string

const attachmentsContextKey contextKey = "ai_attachments"

// WithAttachments stores attachments in the context for the current request.
func WithAttachments(ctx context.Context, attachments []Attachment) context.Context {
	return context.WithValue(ctx, attachmentsContextKey, attachments)
}

// AttachmentsFromContext retrieves attachments from the context, or nil if none.
func AttachmentsFromContext(ctx context.Context) []Attachment {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(attachmentsContextKey).([]Attachment); ok {
		return v
	}
	return nil
}
