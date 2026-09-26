package ai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"context"

	"conduit/internal/auth/oauthflow"
	"conduit/internal/config"
)

// AnthropicProvider implements the Anthropic API
type AnthropicProvider struct {
	name    string
	apiKey  string
	model   string
	authCfg *config.AuthConfig
	client  *http.Client
	baseURL string // conduit-3dru: defaults to https://api.anthropic.com, overridable for proxies/tests
	isOAuth bool
	caching config.PromptCachingConfig // conduit-3dru: prompt caching behavior
	oauthMu sync.Mutex                 // protects apiKey, authCfg fields during OAuth refresh
	retry   retryPolicy                // conduit-31jg.46: 429/529/5xx backoff
}

// isOAuthToken detects if the token is an OAuth token (Pro/Max subscription)
// OAuth tokens have the prefix "sk-ant-oat" (e.g., sk-ant-oat01-...)
func isOAuthToken(token string) bool {
	return strings.Contains(token, "sk-ant-oat")
}

// NewAnthropicProvider creates a new Anthropic provider.
// Token resolution priority:
//  1. ~/.conduit/auth.json (CLI-acquired OAuth token)
//  2. Config auth.oauth_token / ANTHROPIC_OAUTH_TOKEN env var
//  3. Config api_key / ANTHROPIC_API_KEY env var
func NewAnthropicProvider(cfg config.ProviderConfig) (*AnthropicProvider, error) {
	var authToken string
	var authCfg *config.AuthConfig

	// 1. Try loading CLI-stored OAuth token first.
	if stored, err := oauthflow.LoadProviderToken("anthropic"); err == nil && stored != nil && !stored.IsExpired() {
		log.Printf("[Anthropic] Using OAuth token from ~/.conduit/auth.json")
		authToken = stored.AccessToken
		authCfg = &config.AuthConfig{
			Type:         "oauth",
			OAuthToken:   stored.AccessToken,
			RefreshToken: stored.RefreshToken,
			ExpiresAt:    stored.ExpiresAt,
			ClientID:     stored.ClientID,
		}
	}

	// 2. Fall back to config / env var.
	if authToken == "" {
		if cfg.Auth != nil && cfg.Auth.Type == "oauth" && cfg.Auth.OAuthToken != "" {
			authToken = cfg.Auth.OAuthToken
			authCfg = cfg.Auth
		} else if cfg.APIKey != "" {
			authToken = cfg.APIKey
		} else {
			return nil, fmt.Errorf("either OAuth token or API key is required for Anthropic provider")
		}
	}

	// Detect if this is an OAuth token based on prefix.
	isOAuth := isOAuthToken(authToken)

	// conduit-3dru: resolve prompt caching config; nil = defaults (enabled).
	caching := config.DefaultPromptCachingConfig()
	if cfg.PromptCaching != nil {
		caching = *cfg.PromptCaching
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	// bd-29i: Use per-provider timeout with 300s default
	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = 300
	}

	return &AnthropicProvider{
		name:    cfg.Name,
		apiKey:  authToken,
		model:   cfg.Model,
		authCfg: authCfg,
		client:  &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
		baseURL: baseURL,
		isOAuth: isOAuth,
		caching: caching,
		retry:   defaultAnthropicRetryPolicy,
	}, nil
}

func (a *AnthropicProvider) Name() string {
	return a.name
}

// buildMessagesRequest builds the Messages API request body for req and
// returns it with the resolved model. conduit-31jg.12: this is the ONE request
// builder for GenerateResponse and GenerateResponseStreaming — the streaming
// path used to hand-roll its own body and drifted (hardcoded max_tokens, no
// cache breakpoints, mid-conversation system messages dropped). Streaming
// callers add "stream": true to the returned map.
func (a *AnthropicProvider) buildMessagesRequest(req *GenerateRequest) (map[string]interface{}, string) {
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

	// Build messages, injecting Claude Code identity for OAuth
	messages := req.Messages
	var systemBlocks []map[string]interface{}

	if a.isOAuth {
		// OAuth requires Claude Code identity as first system block
		systemBlocks = append(systemBlocks, map[string]interface{}{
			"type": "text",
			"text": "You are Claude Code, Anthropic's official CLI for Claude.",
		})
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
	for _, msg := range messages {
		if msg.Role == "system" {
			for _, blk := range systemMessageBlocks(msg) {
				systemBlocks = append(systemBlocks, map[string]interface{}{
					"type": "text",
					"text": blk.Text,
				})
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
	messages = filteredMessages

	// Convert messages to Anthropic format (handles tool results)
	anthropicMessages := a.convertMessagesToAnthropic(messages)

	anthropicReq := map[string]interface{}{
		"model":      modelToUse,
		"max_tokens": maxTokens,
		"messages":   anthropicMessages,
	}

	// Add tools if provided (with OAuth name mapping if needed)
	var convertedTools []interface{}
	if len(req.Tools) > 0 {
		convertedTools = a.convertToolsToAnthropic(req.Tools)
		if len(convertedTools) > 0 {
			anthropicReq["tools"] = convertedTools
		}
	}

	// Apply cache breakpoints BEFORE serializing the system prompt:
	// conduit-3dru — previously markers were added after API-key auth had
	// already flattened systemBlocks to a plain string, silently dropping
	// the system breakpoint (the largest cacheable prefix) for API-key users.
	a.addCacheBreakpoints(convertedTools, systemBlocks, staticEnd, anthropicMessages, modelToUse)

	// Detect whether any system block now carries a cache marker; if so the
	// block-array form must be preserved even for API-key auth, because
	// cache_control cannot be expressed in the plain-string system form.
	systemHasCacheMarker := false
	for _, block := range systemBlocks {
		if _, ok := block["cache_control"]; ok {
			systemHasCacheMarker = true
			break
		}
	}

	// Add system prompt - as array for OAuth, string for API key
	if len(systemBlocks) > 0 {
		if a.isOAuth || systemHasCacheMarker {
			anthropicReq["system"] = systemBlocks
		} else {
			// For API key auth without cache markers, use simple string format
			var systemText string
			for _, block := range systemBlocks {
				if text, ok := block["text"].(string); ok {
					if systemText != "" {
						systemText += "\n\n"
					}
					systemText += text
				}
			}
			anthropicReq["system"] = systemText
		}
	}

	return anthropicReq, modelToUse
}

// newMessagesHTTPRequest serializes body and builds the POST to
// {baseURL}/v1/messages with auth headers. conduit-31jg.12: shared by both
// paths — streaming used to hardcode https://api.anthropic.com (ignoring a
// configured base_url/proxy) and read a.apiKey without oauthMu.
func (a *AnthropicProvider) newMessagesHTTPRequest(ctx context.Context, body map[string]interface{}, stream bool) (*http.Request, error) {
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", bytes.NewBuffer(reqBody))
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

func (a *AnthropicProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	// Refresh OAuth token if needed
	if err := a.refreshOAuthToken(); err != nil {
		return nil, fmt.Errorf("failed to refresh OAuth token: %w", err)
	}

	// conduit-31jg.12: shared with GenerateResponseStreaming.
	anthropicReq, modelToUse := a.buildMessagesRequest(req)
	// conduit-31jg.46: bounded, jittered retry for 429/529/5xx honouring
	// retry-after and the ctx deadline.
	resp, err := a.postMessagesWithRetry(ctx, anthropicReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var anthropicResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&anthropicResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Log the model from API response (confirms what Anthropic actually used)
	respModel := ""
	if m, ok := anthropicResp["model"].(string); ok {
		respModel = m
	}

	// Parity check: requested vs served model. conduit-31jg.16: LOG ONLY.
	// The old check split on "-2025" (so 2024/2026 snapshots behind an alias
	// looked like a mismatch) and replaced the real reply — content and tool
	// calls — with a warning string returned as a success.
	if modelToUse != "" && respModel != "" && !anthropicModelsMatch(modelToUse, respModel) {
		log.Printf("[Anthropic] WARNING: model mismatch: requested %q, served %q — keeping the response (conduit-31jg.16)", modelToUse, respModel)
	}

	// Extract content and tool calls from Anthropic response format
	content, toolCalls := a.parseAnthropicContent(anthropicResp)
	usage := a.parseAnthropicUsage(anthropicResp)

	// Log cache statistics for debugging and monitoring
	if usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0 {
		totalInput := usage.PromptTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		var hitRate float64
		if totalInput > 0 {
			hitRate = float64(usage.CacheReadInputTokens) / float64(totalInput) * 100
		}
		log.Printf("[Anthropic] Cache stats - Write: %d tokens, Read: %d tokens (%.1f%% hit rate)",
			usage.CacheCreationInputTokens,
			usage.CacheReadInputTokens,
			hitRate)
	}

	result := &GenerateResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Usage:     usage,
	}
	// conduit-31jg.11: map stop_reason so the bd-1k3o length guard, refusal
	// handling and truncated-tool_use dropping work for Anthropic too.
	stopReason, _ := anthropicResp["stop_reason"].(string)
	applyAnthropicStopReason(result, stopReason, lastContentBlockType(anthropicResp) == "tool_use")
	return result, nil
}

// mapAnthropicStopReason normalizes an Anthropic stop_reason into the
// FinishReason vocabulary the rest of the codebase already uses (the OpenAI
// finish_reason values documented on GenerateResponse.FinishReason).
// conduit-31jg.11.
func mapAnthropicStopReason(stopReason string) string {
	switch stopReason {
	case "":
		return ""
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	case "pause_turn":
		// Only produced when Anthropic server tools (web_search_2025xxxx,
		// etc.) run a long turn. Conduit sends only custom tools
		// (convertToolsToAnthropic) and the map-based parser does not keep
		// server_tool_use blocks, so the paused turn cannot be faithfully
		// resent to resume it. Treat it as a final answer.
		return "stop"
	default:
		return stopReason
	}
}

// refusalFallbackContent is delivered when the model refuses with no text,
// so the turn ends visibly instead of tripping the empty-response retry.
const refusalFallbackContent = "The model declined to respond to this request (stop_reason: refusal)."

// applyAnthropicStopReason records the raw and normalized stop reason on resp
// and enforces the invariants the tool loop relies on (conduit-31jg.11):
//
//   - max_tokens with a trailing tool_use: that tool call was cut off
//     mid-input, so executing it would run a tool with partial/empty
//     arguments. It is dropped. FinishReason stays "length", so when nothing
//     else is left to execute the existing bd-1k3o auto-continue takes over
//     (or, with no text either, the conduit-18vj empty guard). Earlier
//     tool_use blocks in the same response are complete and are kept.
//   - refusal: tool calls are dropped (nothing from a refused turn runs) and
//     an empty body gets a visible explanation, making it a terminal answer
//     rather than an empty response that would be retried.
//   - pause_turn: final (see mapAnthropicStopReason).
func applyAnthropicStopReason(resp *GenerateResponse, stopReason string, lastBlockIsToolUse bool) {
	resp.StopReason = stopReason
	resp.FinishReason = mapAnthropicStopReason(stopReason)

	switch stopReason {
	case "max_tokens", "model_context_window_exceeded":
		if lastBlockIsToolUse && len(resp.ToolCalls) > 0 {
			dropped := resp.ToolCalls[len(resp.ToolCalls)-1]
			resp.ToolCalls = resp.ToolCalls[:len(resp.ToolCalls)-1]
			if len(resp.ToolCalls) == 0 {
				resp.ToolCalls = nil
			}
			log.Printf("[Anthropic] WARNING: tool_use %q (id=%s) truncated by stop_reason=%s — dropped, not executed (conduit-31jg.11)",
				dropped.Name, dropped.ID, stopReason)
		}
	case "refusal":
		if len(resp.ToolCalls) > 0 {
			log.Printf("[Anthropic] stop_reason=refusal with %d tool call(s) — dropping them (conduit-31jg.11)", len(resp.ToolCalls))
			resp.ToolCalls = nil
		}
		if strings.TrimSpace(resp.Content) == "" {
			resp.Content = refusalFallbackContent
		}
	case "pause_turn":
		log.Printf("[Anthropic] stop_reason=pause_turn — treating as final, no server tools in use (conduit-31jg.11)")
	}
}

// lastContentBlockType returns the type of the final content block in a
// non-streaming Messages API response, or "" if unavailable.
func lastContentBlockType(resp map[string]interface{}) string {
	blocks, ok := resp["content"].([]interface{})
	if !ok || len(blocks) == 0 {
		return ""
	}
	last, ok := blocks[len(blocks)-1].(map[string]interface{})
	if !ok {
		return ""
	}
	t, _ := last["type"].(string)
	return t
}

// convertMessagesToAnthropic converts messages to Anthropic API format
// This handles the special case of tool results which must be sent as user messages
func (a *AnthropicProvider) convertMessagesToAnthropic(messages []ChatMessage) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(messages))

	for _, msg := range messages {
		before := len(result)
		switch msg.Role {
		case "user":
			if len(msg.Attachments) > 0 {
				contentBlocks := make([]map[string]interface{}, 0, len(msg.Attachments)+1)
				for _, att := range msg.Attachments {
					if att.Type == "image" && len(att.Data) > 0 {
						contentBlocks = append(contentBlocks, map[string]interface{}{
							"type": "image",
							"source": map[string]interface{}{
								"type":       "base64",
								"media_type": att.MediaType,
								"data":       base64.StdEncoding.EncodeToString(att.Data),
							},
						})
					}
				}
				if msg.Content != "" {
					contentBlocks = append(contentBlocks, map[string]interface{}{
						"type": "text",
						"text": msg.Content,
					})
				}
				if len(contentBlocks) > 0 {
					result = append(result, map[string]interface{}{
						"role":    "user",
						"content": contentBlocks,
					})
				}
			} else {
				result = append(result, map[string]interface{}{
					"role":    "user",
					"content": msg.Content,
				})
			}
		case "assistant":
			// Build assistant message with potential tool_use blocks
			if len(msg.ToolCalls) > 0 {
				content := make([]map[string]interface{}, 0)
				if msg.Content != "" {
					content = append(content, map[string]interface{}{
						"type": "text",
						"text": msg.Content,
					})
				}
				for _, tc := range msg.ToolCalls {
					// Ensure tool input is always a valid JSON object for OAuth
					input := tc.Args
					if input == nil {
						input = make(map[string]interface{})
					}
					content = append(content, map[string]interface{}{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Name,
						"input": input,
					})
				}
				result = append(result, map[string]interface{}{
					"role":    "assistant",
					"content": content,
				})
			} else {
				result = append(result, map[string]interface{}{
					"role":    "assistant",
					"content": msg.Content,
				})
			}
		case "tool":
			// Tool results must be sent as user messages with tool_result content.
			// conduit-31jg.45: failed calls carry is_error, and all results of
			// one round share ONE user message (the API requires every
			// tool_result for an assistant turn in the next user turn; we no
			// longer rely on it merging consecutive user turns).
			block := map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": msg.ToolCallID,
				"content":     msg.Content,
			}
			if msg.IsError {
				block["is_error"] = true
			}
			if blocks := trailingToolResultBlocks(result); blocks != nil {
				result[len(result)-1]["content"] = append(blocks, block)
			} else {
				result = append(result, map[string]interface{}{
					"role":    "user",
					"content": []map[string]interface{}{block},
				})
			}
		}

		// conduit-31jg.45: user text that follows a round's tool results
		// (loop guidance, refocus) joins that same user message after the
		// tool_result blocks instead of forming a second user turn.
		if msg.Role == "user" && before > 0 && len(result) == before+1 {
			if blocks := trailingToolResultBlocks(result[:len(result)-1]); blocks != nil {
				result[len(result)-2]["content"] = append(blocks, userContentBlocks(result[len(result)-1]["content"])...)
				result = result[:len(result)-1]
			}
		}
	}

	return result
}

// trailingToolResultBlocks returns the content blocks of the last converted
// message when it is a user turn carrying tool_result blocks, else nil.
// conduit-31jg.45.
func trailingToolResultBlocks(converted []map[string]interface{}) []map[string]interface{} {
	if len(converted) == 0 {
		return nil
	}
	last := converted[len(converted)-1]
	if last["role"] != "user" {
		return nil
	}
	blocks, ok := last["content"].([]map[string]interface{})
	if !ok || len(blocks) == 0 || blocks[0]["type"] != "tool_result" {
		return nil
	}
	return blocks
}

// userContentBlocks normalizes converted user content (string or blocks) to
// a block slice. conduit-31jg.45.
func userContentBlocks(content interface{}) []map[string]interface{} {
	switch c := content.(type) {
	case []map[string]interface{}:
		return c
	case string:
		if c == "" {
			return nil
		}
		return []map[string]interface{}{{"type": "text", "text": c}}
	}
	return nil
}

// Claude Code tool names that are known to work with OAuth tokens
var claudeCodeTools = map[string]bool{
	"Read": true, "Write": true, "Edit": true, "Bash": true,
	"Grep": true, "Glob": true, "WebFetch": true, "WebSearch": true,
	"AskUserQuestion": true, "EnterPlanMode": true, "ExitPlanMode": true,
	"KillShell": true, "NotebookEdit": true, "Skill": true, "Task": true,
	"TaskOutput": true, "TodoWrite": true,
}

// convertToolsToAnthropic converts tool definitions to Anthropic format
// When using OAuth tokens, only Claude Code-compatible tools are included
func (a *AnthropicProvider) convertToolsToAnthropic(tools []Tool) []interface{} {
	anthropicTools := make([]interface{}, 0, len(tools))

	for _, tool := range tools {
		// For OAuth tokens, only include Claude Code-compatible tools
		if a.isOAuth && !claudeCodeTools[tool.Name] {
			continue
		}

		anthropicTools = append(anthropicTools, map[string]interface{}{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.Parameters,
		})
	}
	return anthropicTools
}

// parseAnthropicContent extracts content and tool calls from Anthropic response
func (a *AnthropicProvider) parseAnthropicContent(resp map[string]interface{}) (string, []ToolCall) {
	var content strings.Builder
	var toolCalls []ToolCall

	if contentArray, ok := resp["content"].([]interface{}); ok {
		for _, item := range contentArray {
			if contentObj, ok := item.(map[string]interface{}); ok {
				if contentType, ok := contentObj["type"].(string); ok {
					switch contentType {
					case "text":
						if text, ok := contentObj["text"].(string); ok {
							if content.Len() > 0 {
								content.WriteString("\n")
							}
							content.WriteString(text)
						}
					case "tool_use":
						// Parse tool call
						if toolCall := a.parseAnthropicToolCall(contentObj); toolCall != nil {
							toolCalls = append(toolCalls, *toolCall)
						}
					}
				}
			}
		}
	}

	return content.String(), toolCalls
}

// parseAnthropicToolCall extracts a tool call from Anthropic tool_use block
func (a *AnthropicProvider) parseAnthropicToolCall(toolObj map[string]interface{}) *ToolCall {
	id, hasID := toolObj["id"].(string)
	name, hasName := toolObj["name"].(string)
	input, hasInput := toolObj["input"].(map[string]interface{})

	if !hasID || !hasName || !hasInput {
		return nil
	}

	// Tool names now match Claude Code format directly, no conversion needed

	return &ToolCall{
		ID:   id,
		Name: name,
		Args: input,
	}
}

// parseAnthropicUsage extracts usage statistics from Anthropic response
func (a *AnthropicProvider) parseAnthropicUsage(resp map[string]interface{}) Usage {
	var usage Usage

	if usageObj, ok := resp["usage"].(map[string]interface{}); ok {
		if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
			usage.PromptTokens = int(inputTokens)
		}
		if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
			usage.CompletionTokens = int(outputTokens)
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

		// Parse cache metrics from Anthropic response
		if cacheCreate, ok := usageObj["cache_creation_input_tokens"].(float64); ok {
			usage.CacheCreationInputTokens = int(cacheCreate)
		}
		if cacheRead, ok := usageObj["cache_read_input_tokens"].(float64); ok {
			usage.CacheReadInputTokens = int(cacheRead)
		}
	}

	return usage
}

// maxCacheBreakpoints is the Anthropic limit on cache_control markers per
// request (tools + system + messages combined). conduit-31jg.14.
const maxCacheBreakpoints = 4

// imageTokenEstimate is a flat per-image estimate; base64 length would
// wildly overstate an image's token cost.
const imageTokenEstimate = 1600

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

// estimateTokens is the rough 4-chars-per-token estimate used for cache
// thresholds.
func estimateTokens(s string) int { return len(s) / 4 }

// estimateJSONTokens estimates the tokens of an arbitrary request fragment.
func estimateJSONTokens(v interface{}) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b) / 4
}

// estimateMessageTokens estimates one converted message. conduit-31jg.14:
// the old estimate counted only string content, so tool turns (block
// arrays: tool_use / tool_result / text) counted as zero.
func estimateMessageTokens(msg map[string]interface{}) int {
	switch c := msg["content"].(type) {
	case string:
		return estimateTokens(c)
	case []map[string]interface{}:
		n := 0
		for _, block := range c {
			if block["type"] == "image" {
				n += imageTokenEstimate
				continue
			}
			n += estimateJSONTokens(block)
		}
		return n
	}
	return 0
}

// markMessageBreakpoint puts cc on one content block of msg and reports
// whether it did. String content is converted to a single text block. In a
// block array the last tool_result is preferred: text after it is ephemeral
// loop guidance (conduit-31jg.13) that the next request strips, so caching
// through it would write an entry that is never read.
func markMessageBreakpoint(msg map[string]interface{}, cc map[string]interface{}) bool {
	switch c := msg["content"].(type) {
	case string:
		if c == "" {
			return false
		}
		msg["content"] = []map[string]interface{}{
			{"type": "text", "text": c, "cache_control": cc},
		}
		return true
	case []map[string]interface{}:
		target := -1
		for i := len(c) - 1; i >= 0; i-- {
			if c[i]["type"] == "tool_result" {
				target = i
				break
			}
		}
		if target < 0 {
			target = len(c) - 1
		}
		if target < 0 {
			return false
		}
		if c[target]["type"] == "text" {
			if text, _ := c[target]["text"].(string); text == "" {
				return false
			}
		}
		c[target]["cache_control"] = cc
		return true
	}
	return false
}

// addCacheBreakpoints adds cache_control markers to the request components
// based on the configured PromptCachingConfig (conduit-3dru). The master
// switch gates everything; granular flags gate each breakpoint type.
//
// conduit-31jg.14: at most maxCacheBreakpoints markers, placed as
//  1. last tool definition (CacheTools)
//  2. systemBlocks[staticEnd] — the last byte-stable system block; dynamic
//     blocks after it (timestamp etc.) are outside the cached prefix
//  3. the LAST message — rolling breakpoint, so each tool-loop round trip
//     reads everything the previous one wrote (CacheHistory)
//  4. an anchor HistoryBreakpointInterval messages back, a second read
//     point when a round adds more than the ~20-block lookback (CacheHistory)
//
// Each is placed only when the estimated prefix up to it (tools → system →
// messages, in API order) reaches the model's minimum cacheable length.
func (a *AnthropicProvider) addCacheBreakpoints(
	tools []interface{},
	systemBlocks []map[string]interface{},
	staticEnd int,
	messages []map[string]interface{},
	model string,
) {
	if !a.caching.Enabled {
		return
	}
	minTokens := GetCacheMinTokens(model)
	cacheControl := map[string]interface{}{"type": "ephemeral"}
	if a.caching.ExtendedTTL {
		cacheControl["ttl"] = "1h"
	}

	used := 0
	canMark := func() bool { return used < maxCacheBreakpoints }

	// prefix is the running estimate of everything before the next marker.
	prefix := 0

	// Breakpoint 1: last tool definition.
	if len(tools) > 0 {
		prefix += estimateJSONTokens(tools)
		if a.caching.CacheTools && prefix >= minTokens && canMark() {
			if lastTool, ok := tools[len(tools)-1].(map[string]interface{}); ok {
				lastTool["cache_control"] = cacheControl
				used++
			}
		}
	}

	// Breakpoint 2: last static system block.
	for i, block := range systemBlocks {
		text, _ := block["text"].(string)
		prefix += estimateTokens(text)
		if i == staticEnd && a.caching.CacheSystem && prefix >= minTokens && canMark() {
			block["cache_control"] = cacheControl
			used++
		}
	}

	if !a.caching.CacheHistory || len(messages) == 0 {
		return
	}

	// Breakpoints 3 and 4: conversation history.
	msgPrefix := make([]int, len(messages)) // estimated prefix through message i
	running := prefix
	for i, msg := range messages {
		running += estimateMessageTokens(msg)
		msgPrefix[i] = running
	}

	last := len(messages) - 1
	if msgPrefix[last] >= minTokens && canMark() {
		if markMessageBreakpoint(messages[last], cacheControl) {
			used++
		}
	}

	interval := a.caching.HistoryBreakpointInterval
	if interval <= 0 {
		interval = 6
	}
	anchor := last - interval
	if anchor >= 0 && msgPrefix[anchor] >= minTokens && canMark() {
		if markMessageBreakpoint(messages[anchor], cacheControl) {
			used++
		}
	}
}

// refreshOAuthToken refreshes the OAuth token if needed.
func (a *AnthropicProvider) refreshOAuthToken() error {
	a.oauthMu.Lock()
	defer a.oauthMu.Unlock()

	if a.authCfg == nil || a.authCfg.Type != "oauth" || a.authCfg.RefreshToken == "" {
		return nil // No refresh needed for API key auth
	}

	// Check if token needs refresh (expires within 5 minutes).
	if a.authCfg.ExpiresAt > time.Now().Add(5*time.Minute).Unix() {
		return nil // Token is still valid
	}

	log.Printf("[Anthropic] OAuth token expiring soon, refreshing...")

	newToken, err := oauthflow.RefreshToken(a.authCfg.RefreshToken, a.authCfg.ClientID)
	if err != nil {
		return fmt.Errorf("token refresh failed: %w", err)
	}

	// Update in-memory state.
	a.apiKey = newToken.AccessToken
	a.authCfg.OAuthToken = newToken.AccessToken
	a.authCfg.ExpiresAt = newToken.ExpiresAt
	if newToken.RefreshToken != "" {
		a.authCfg.RefreshToken = newToken.RefreshToken
	}

	// Persist to disk.
	if err := oauthflow.SaveProviderToken("anthropic", newToken); err != nil {
		log.Printf("[Anthropic] WARNING: refreshed token in memory but failed to save to disk: %v", err)
	}

	log.Printf("[Anthropic] OAuth token refreshed, new expiry: %s",
		time.Unix(newToken.ExpiresAt, 0).Format(time.RFC3339))
	return nil
}
