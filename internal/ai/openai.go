package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"conduit/internal/config"
)

const defaultOpenAIURL = "https://api.openai.com/v1/chat/completions"

const (
	maxRetries     = 3
	retryBaseDelay = 2 * time.Second
)

// isRetryableStatus returns true for HTTP status codes that warrant a retry.
func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || // 429
		code == http.StatusBadGateway || // 502
		code == http.StatusServiceUnavailable || // 503
		code == http.StatusGatewayTimeout || // 504
		code == 500 // Internal Server Error
}

// OpenAIProvider implements the OpenAI API (and any OpenAI-compatible server)
type OpenAIProvider struct {
	name    string
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewOpenAIProvider creates a new OpenAI-compatible provider.
// API key is only required when targeting api.openai.com (i.e. no custom base_url).
func NewOpenAIProvider(cfg config.ProviderConfig) (*OpenAIProvider, error) {
	baseURL := cfg.BaseURL

	if baseURL == "" {
		// Targeting OpenAI proper — key is required
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("API key is required for OpenAI provider (set base_url for local servers)")
		}
		baseURL = defaultOpenAIURL
	} else {
		// Normalize: ensure the URL ends with /chat/completions
		baseURL = normalizeOpenAIBaseURL(baseURL)
	}

	return &OpenAIProvider{
		name:    cfg.Name,
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// normalizeOpenAIBaseURL ensures the URL ends with /chat/completions.
// Users typically provide just the base (e.g. http://localhost:11434/v1).
func normalizeOpenAIBaseURL(u string) string {
	u = strings.TrimRight(u, "/")
	if strings.HasSuffix(u, "/chat/completions") {
		return u
	}
	return u + "/chat/completions"
}

func (o *OpenAIProvider) Name() string {
	return o.name
}

// stripProviderPrefix removes a "provider/" prefix from a model name.
// e.g. "ghost/Qwen3.5-9B-Q6_K" → "Qwen3.5-9B-Q6_K", "claude-sonnet" → "claude-sonnet"
func stripProviderPrefix(model string) string {
	if idx := strings.Index(model, "/"); idx > 0 {
		return model[idx+1:]
	}
	return model
}

func (o *OpenAIProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	model := o.model
	if req.Model != "" {
		model = stripProviderPrefix(req.Model)
	}

	openaiReq := map[string]interface{}{
		"model":      model,
		"messages":   o.convertMessagesToOpenAI(req.Messages),
		"max_tokens": req.MaxTokens,
	}

	if len(req.Tools) > 0 {
		openaiReq["tools"] = o.convertToolsToOpenAI(req.Tools)
		openaiReq["tool_choice"] = "auto"
	}

	reqBody, err := json.Marshal(openaiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	var lastErr error
	var resp *http.Response
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryBaseDelay * time.Duration(1<<(attempt-1))
			log.Printf("[OpenAI] Retry %d/%d after %v for status error", attempt, maxRetries, delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// Rebuild request body (previous Do consumed it)
			httpReq, err = http.NewRequestWithContext(ctx, "POST", o.baseURL, bytes.NewBuffer(reqBody))
			if err != nil {
				return nil, fmt.Errorf("failed to create request: %w", err)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			if o.apiKey != "" {
				httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
			}
		}

		resp, err = o.client.Do(httpReq)
		if err != nil {
			// bd-13p: transport errors (timeouts, connection resets) are
			// transient — enter the retry loop instead of failing fast.
			// Only a caller that gave up (ctx cancelled) aborts immediately.
			if isCallerContextError(ctx, err) {
				return nil, fmt.Errorf("request failed: %w", err)
			}
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			break // success
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = fmt.Errorf("API error: %d - %s", resp.StatusCode, string(bodyBytes))

		if !isRetryableStatus(resp.StatusCode) {
			return nil, lastErr // non-retryable, fail immediately
		}
		// retryable — loop continues
	}

	// Transport errors on every attempt leave resp nil — surface lastErr
	// instead of dereferencing nil (bd-13p).
	if resp == nil {
		return nil, lastErr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, lastErr
	}
	defer resp.Body.Close()

	var openaiResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	content, toolCalls := o.parseOpenAIContent(openaiResp)
	usage := o.parseOpenAIUsage(openaiResp)

	return &GenerateResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Usage:     usage,
	}, nil
}

// GenerateResponseStreaming implements StreamingProvider for OpenAI-compatible servers.
func (o *OpenAIProvider) GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error) {
	model := o.model
	if req.Model != "" {
		model = stripProviderPrefix(req.Model)
	}

	openaiReq := map[string]interface{}{
		"model":      model,
		"messages":   o.convertMessagesToOpenAI(req.Messages),
		"max_tokens": req.MaxTokens,
		"stream":     true,
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	if len(req.Tools) > 0 {
		openaiReq["tools"] = o.convertToolsToOpenAI(req.Tools)
		openaiReq["tool_choice"] = "auto"
	}

	reqBody, err := json.Marshal(openaiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	log.Printf("[OpenAI] Streaming request: model=%s, url=%s", model, o.baseURL)

	var lastErr error
	var resp *http.Response
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryBaseDelay * time.Duration(1<<(attempt-1))
			log.Printf("[OpenAI] Streaming retry %d/%d after %v for status error", attempt, maxRetries, delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// Rebuild request body (previous Do consumed it)
			httpReq, err = http.NewRequestWithContext(ctx, "POST", o.baseURL, bytes.NewBuffer(reqBody))
			if err != nil {
				return nil, fmt.Errorf("failed to create request: %w", err)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Accept", "text/event-stream")
			if o.apiKey != "" {
				httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
			}
		}

		resp, err = o.client.Do(httpReq)
		if err != nil {
			// bd-13p: transport errors (timeouts, connection resets) are
			// transient — enter the retry loop instead of failing fast.
			// Only a caller that gave up (ctx cancelled) aborts immediately.
			if isCallerContextError(ctx, err) {
				return nil, fmt.Errorf("request failed: %w", err)
			}
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			break // success
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = fmt.Errorf("API error: %d - %s", resp.StatusCode, string(bodyBytes))

		if !isRetryableStatus(resp.StatusCode) {
			return nil, lastErr // non-retryable, fail immediately
		}
		log.Printf("[OpenAI] Streaming retryable error: %v", lastErr)
		// retryable — loop continues
	}

	// Transport errors on every attempt leave resp nil — surface lastErr
	// instead of dereferencing nil (bd-13p).
	if resp == nil {
		return nil, lastErr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, lastErr
	}
	defer resp.Body.Close()

	return o.parseOpenAISSEStream(resp.Body, onDelta)
}

// parseOpenAISSEStream parses OpenAI-format Server-Sent Events.
// Text: data: {"choices":[{"delta":{"content":"token"}}]}
// Tool calls: incremental by index via delta.tool_calls[].function.{name,arguments}
// Done: data: [DONE]
func (o *OpenAIProvider) parseOpenAISSEStream(body io.Reader, onDelta StreamCallback) (*GenerateResponse, error) {
	scanner := bufio.NewScanner(body)

	var contentBuilder strings.Builder
	var usage Usage

	// Tool calls are assembled incrementally by index
	type pendingToolCall struct {
		id        string
		name      string
		arguments strings.Builder
	}
	toolCallMap := make(map[int]*pendingToolCall) // index → pending

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimSpace(data)

		if data == "" {
			continue
		}

		if data == "[DONE]" {
			if onDelta != nil {
				onDelta("", true)
			}
			continue
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Printf("[OpenAI] Failed to parse SSE chunk: %v", err)
			continue
		}

		// Extract usage from chunk (sent when stream_options.include_usage is true)
		if usageObj, ok := chunk["usage"].(map[string]interface{}); ok {
			usage = parseUsageMap(usageObj)
		}

		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}

		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}

		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}

		// Text content delta
		if text, ok := delta["content"].(string); ok && text != "" {
			contentBuilder.WriteString(text)
			if onDelta != nil {
				onDelta(text, false)
			}
		}

		// Tool call deltas (incremental by index)
		if tcArray, ok := delta["tool_calls"].([]interface{}); ok {
			for _, tcRaw := range tcArray {
				tcObj, ok := tcRaw.(map[string]interface{})
				if !ok {
					continue
				}

				idx := int(getFloat64(tcObj, "index"))

				pending, exists := toolCallMap[idx]
				if !exists {
					pending = &pendingToolCall{}
					toolCallMap[idx] = pending
				}

				if id, ok := tcObj["id"].(string); ok && id != "" {
					pending.id = id
				}

				if fn, ok := tcObj["function"].(map[string]interface{}); ok {
					if name, ok := fn["name"].(string); ok && name != "" {
						pending.name = name
					}
					if args, ok := fn["arguments"].(string); ok {
						pending.arguments.WriteString(args)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading stream: %w", err)
	}

	// Assemble tool calls from pending map
	var toolCalls []ToolCall
	for i := 0; i < len(toolCallMap); i++ {
		pending, ok := toolCallMap[i]
		if !ok {
			continue
		}
		var args map[string]interface{}
		if argsStr := pending.arguments.String(); argsStr != "" {
			if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
				log.Printf("[OpenAI] Failed to parse tool call args: %v", err)
			}
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:   pending.id,
			Name: pending.name,
			Args: args,
		})
	}

	// Ensure TotalTokens is computed
	if usage.TotalTokens == 0 && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	return &GenerateResponse{
		Content:   contentBuilder.String(),
		ToolCalls: toolCalls,
		Usage:     usage,
	}, nil
}

// parseUsageMap extracts Usage from an OpenAI usage JSON object.
func parseUsageMap(m map[string]interface{}) Usage {
	return Usage{
		PromptTokens:     int(getFloat64(m, "prompt_tokens")),
		CompletionTokens: int(getFloat64(m, "completion_tokens")),
		TotalTokens:      int(getFloat64(m, "total_tokens")),
	}
}

// convertToolsToOpenAI converts tool definitions to OpenAI format
func (o *OpenAIProvider) convertToolsToOpenAI(tools []Tool) []interface{} {
	openaiTools := make([]interface{}, len(tools))
	for i, tool := range tools {
		openaiTools[i] = map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		}
	}
	return openaiTools
}

// convertMessagesToOpenAI converts ChatMessage slice to OpenAI format.
// This handles tool calls (which need type:"function" and nested function object)
// and tool results (which use role:"tool" with tool_call_id).
//
// System messages are extracted and consolidated at the beginning of the array.
// This is required because some backends (llama.cpp, vLLM) enforce that system
// messages appear only at the start, but the tool execution engine may inject
// mid-conversation system messages (goal refocus, failure pivot suggestions).
func (o *OpenAIProvider) convertMessagesToOpenAI(messages []ChatMessage) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(messages))

	// First pass: collect any system messages that appear after index 0.
	// Consolidate them into the leading system message block.
	var systemParts []string
	var nonSystemMessages []ChatMessage
	for _, msg := range messages {
		if msg.Role == "system" {
			systemParts = append(systemParts, msg.Content)
		} else {
			nonSystemMessages = append(nonSystemMessages, msg)
		}
	}

	// Emit consolidated system message first (if any).
	if len(systemParts) > 0 {
		result = append(result, map[string]interface{}{
			"role":    "system",
			"content": strings.Join(systemParts, "\n\n"),
		})
	}

	for _, msg := range nonSystemMessages {
		converted := map[string]interface{}{
			"role": msg.Role,
		}

		// Handle tool result messages
		if msg.Role == "tool" && msg.ToolCallID != "" {
			converted["tool_call_id"] = msg.ToolCallID
			converted["content"] = msg.Content
			result = append(result, converted)
			continue
		}

		// Handle assistant messages with tool calls
		if len(msg.ToolCalls) > 0 {
			// OpenAI requires content to be null or omitted when there are tool calls
			if msg.Content != "" {
				converted["content"] = msg.Content
			}

			// Convert tool calls to OpenAI format
			openaiToolCalls := make([]map[string]interface{}, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				// Serialize arguments to JSON string as OpenAI expects
				argsJSON, _ := json.Marshal(tc.Args)

				openaiToolCalls[i] = map[string]interface{}{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      tc.Name,
						"arguments": string(argsJSON),
					},
				}
			}
			converted["tool_calls"] = openaiToolCalls
			result = append(result, converted)
			continue
		}

		// Regular message
		if msg.Role == "user" && len(msg.Attachments) > 0 {
			contentBlocks := make([]map[string]interface{}, 0, len(msg.Attachments)+1)
			for _, att := range msg.Attachments {
				if att.Type == "image" && len(att.Data) > 0 {
					dataURI := "data:" + att.MediaType + ";base64," + base64.StdEncoding.EncodeToString(att.Data)
					contentBlocks = append(contentBlocks, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": dataURI,
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
				converted["content"] = contentBlocks
			}
		} else {
			converted["content"] = msg.Content
		}
		result = append(result, converted)
	}

	return result
}

// parseOpenAIContent extracts content and tool calls from OpenAI response
func (o *OpenAIProvider) parseOpenAIContent(resp map[string]interface{}) (string, []ToolCall) {
	var content string
	var toolCalls []ToolCall

	if choices, ok := resp["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if text, ok := message["content"].(string); ok {
					content = text
				}

				if toolCallsArray, ok := message["tool_calls"].([]interface{}); ok {
					for _, tc := range toolCallsArray {
						if toolCallObj, ok := tc.(map[string]interface{}); ok {
							if parsedCall := o.parseOpenAIToolCall(toolCallObj); parsedCall != nil {
								toolCalls = append(toolCalls, *parsedCall)
							}
						}
					}
				}
			}
		}
	}

	return content, toolCalls
}

// parseOpenAIToolCall extracts a tool call from OpenAI tool_calls array
func (o *OpenAIProvider) parseOpenAIToolCall(toolObj map[string]interface{}) *ToolCall {
	id, hasID := toolObj["id"].(string)

	function, hasFunction := toolObj["function"].(map[string]interface{})
	if !hasID || !hasFunction {
		return nil
	}

	name, hasName := function["name"].(string)
	argumentsStr, hasArgs := function["arguments"].(string)
	if !hasName || !hasArgs {
		return nil
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argumentsStr), &args); err != nil {
		return nil
	}

	return &ToolCall{
		ID:   id,
		Name: name,
		Args: args,
	}
}

// parseOpenAIUsage extracts usage statistics from OpenAI response
func (o *OpenAIProvider) parseOpenAIUsage(resp map[string]interface{}) Usage {
	if usageObj, ok := resp["usage"].(map[string]interface{}); ok {
		return parseUsageMap(usageObj)
	}
	return Usage{}
}
