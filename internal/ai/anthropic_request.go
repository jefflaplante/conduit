package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"conduit/internal/models"
)

// claudeCodeIdentity is the system block OAuth (Pro/Max subscription)
// requests must start with.
const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

// buildMessagesRequest builds the Messages API request body for req and
// returns it with the resolved model. conduit-31jg.12: this is the ONE request
// builder for GenerateResponse and GenerateResponseStreaming — the streaming
// path used to hand-roll its own body and drifted (hardcoded max_tokens, no
// cache breakpoints, mid-conversation system messages dropped). Streaming
// callers set Stream on the returned request. conduit-31jg.36: typed
// (models.MessagesRequest) instead of map[string]interface{}.
func (a *AnthropicProvider) buildMessagesRequest(req *GenerateRequest) (*models.MessagesRequest, string) {
	// Determine which model to use
	modelToUse := a.model
	if req.Model != "" {
		modelToUse = req.Model
	}

	// max_tokens is required by the API. A zero value used to 400 on this
	// path and was silently replaced by 16000 on the streaming path.
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultChainMaxTokens
	}

	var systemBlocks []models.ContentBlock
	if a.isOAuth {
		// OAuth requires Claude Code identity as first system block
		systemBlocks = append(systemBlocks, models.TextBlock(claudeCodeIdentity))
	}

	// Extract ALL system messages from the array and consolidate into system blocks.
	// This handles cases where system messages are injected mid-conversation.
	// conduit-31jg.14: the leading system message is expanded into its
	// agent blocks; staticEnd is the last block of the byte-stable prefix
	// (OAuth identity + static agent block), where the system cache
	// breakpoint goes. Dynamic blocks and any later system message come
	// after it, so per-turn content never invalidates the cached prefix.
	staticEnd := len(systemBlocks) - 1
	prefixOpen := true
	var filteredMessages []ChatMessage
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			for _, blk := range systemMessageBlocks(msg) {
				systemBlocks = append(systemBlocks, models.TextBlock(blk.Text))
				if blk.Dynamic {
					prefixOpen = false
				}
				if prefixOpen {
					staticEnd = len(systemBlocks) - 1
				}
			}
			prefixOpen = false
		} else {
			filteredMessages = append(filteredMessages, msg)
		}
	}
	body := &models.MessagesRequest{
		Model:     modelToUse,
		MaxTokens: maxTokens,
		// Convert messages to Anthropic format (handles tool results)
		Messages: a.convertMessagesToAnthropic(filteredMessages),
	}

	// Add tools if provided (OAuth keeps only Claude Code tool names). An
	// empty result is omitted from the wire (omitempty).
	if len(req.Tools) > 0 {
		body.Tools = a.convertToolsToAnthropic(req.Tools)
	}

	// Apply cache breakpoints BEFORE serializing the system prompt:
	// conduit-3dru — previously markers were added after API-key auth had
	// already flattened systemBlocks to a plain string, silently dropping
	// the system breakpoint (the largest cacheable prefix) for API-key users.
	a.addCacheBreakpoints(body.Tools, systemBlocks, staticEnd, body.Messages, modelToUse)

	// Detect whether any system block now carries a cache marker; if so the
	// block-array form must be preserved even for API-key auth, because
	// cache_control cannot be expressed in the plain-string system form.
	systemHasCacheMarker := false
	for _, block := range systemBlocks {
		if block.CacheControl != nil {
			systemHasCacheMarker = true
			break
		}
	}

	// Add system prompt - as array for OAuth, string for API key
	if len(systemBlocks) > 0 {
		if a.isOAuth || systemHasCacheMarker {
			body.System = &models.SystemPrompt{Blocks: systemBlocks}
		} else {
			// For API key auth without cache markers, use simple string format
			var systemText string
			for _, block := range systemBlocks {
				if systemText != "" {
					systemText += "\n\n"
				}
				systemText += block.Text
			}
			body.System = &models.SystemPrompt{Text: systemText}
		}
	}

	return body, modelToUse
}

// marshalMessagesRequest serializes the request body once per call, so every
// retry attempt sends the identical bytes (conduit-31jg.46).
func marshalMessagesRequest(body *models.MessagesRequest) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	return b, nil
}

// newMessagesHTTPRequest builds the POST of the serialized body to
// {baseURL}/v1/messages with auth headers. conduit-31jg.12: shared by both
// paths — streaming used to hardcode https://api.anthropic.com (ignoring a
// configured base_url/proxy) and read a.apiKey without oauthMu.
func (a *AnthropicProvider) newMessagesHTTPRequest(ctx context.Context, body []byte, stream bool) (*http.Request, error) {
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	// Read apiKey under lock to avoid racing with refreshOAuthToken
	a.oauthMu.Lock()
	currentKey := a.apiKey
	a.oauthMu.Unlock()

	// Use OAuth Bearer token or fall back to API key
	if a.isOAuth {
		httpReq.Header.Set("Authorization", "Bearer "+currentKey)
		// Required headers for OAuth tokens - must match Claude Code exactly
		httpReq.Header.Set("accept", accept)
		httpReq.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14")
		httpReq.Header.Set("anthropic-dangerous-direct-browser-access", "true")
		httpReq.Header.Set("user-agent", "claude-cli/2.1.2 (external, cli)")
		httpReq.Header.Set("x-app", "cli")
	} else {
		httpReq.Header.Set("Accept", accept)
		httpReq.Header.Set("x-api-key", currentKey)
	}

	return httpReq, nil
}

// systemMessageBlocks returns the blocks a system ChatMessage contributes.
// The agent's split (static + dynamic) is used only while it still matches
// Content — if something rewrote Content after the prompt was built, the
// joined Content wins as a single static block. Empty blocks are dropped
// (the API rejects empty text blocks). conduit-31jg.14.
func systemMessageBlocks(msg ChatMessage) []SystemBlock {
	if len(msg.SystemBlocks) > 0 {
		texts := make([]string, 0, len(msg.SystemBlocks))
		out := make([]SystemBlock, 0, len(msg.SystemBlocks))
		for _, b := range msg.SystemBlocks {
			texts = append(texts, b.Text)
			if b.Text != "" {
				out = append(out, b)
			}
		}
		if strings.Join(texts, "\n\n") == msg.Content {
			return out
		}
	}
	if msg.Content == "" {
		return nil
	}
	return []SystemBlock{{Type: "text", Text: msg.Content}}
}
