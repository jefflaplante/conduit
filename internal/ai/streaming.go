package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// StreamCallback is called with text deltas during streaming
type StreamCallback func(delta string, done bool)

// StreamingResponse holds accumulated streaming data
type StreamingResponse struct {
	Content   string
	ToolCalls []ToolCall
	Usage     *Usage
}

// generateWithStreamOAuth generates a response using Anthropic's streaming API
// For OAuth tokens, this mimics Claude Code's exact request format
func (a *AnthropicProvider) generateWithStreamOAuth(
	ctx context.Context,
	messages []ChatMessage,
	tools []Tool,
	systemPrompt string,
	modelOverride string,
	onDelta StreamCallback,
) (*GenerateResponse, error) {

	modelToUse := a.model
	if modelOverride != "" {
		modelToUse = modelOverride
	}

	// Build request body
	reqBody := map[string]interface{}{
		"model":      modelToUse,
		"max_tokens": 16000,
		"stream":     true, // Enable streaming
	}

	// For OAuth tokens, system prompt MUST be an array starting with Claude Code identity
	// This is required by Anthropic's OAuth validation
	if a.isOAuth {
		systemBlocks := []map[string]interface{}{
			{
				"type": "text",
				"text": "You are Claude Code, Anthropic's official CLI for Claude.",
			},
		}
		if systemPrompt != "" {
			systemBlocks = append(systemBlocks, map[string]interface{}{
				"type": "text",
				"text": systemPrompt,
			})
		}
		reqBody["system"] = systemBlocks
	} else if systemPrompt != "" {
		reqBody["system"] = systemPrompt
	}

	// Convert messages to Anthropic format
	anthropicMessages := a.convertMessagesToAnthropic(messages)
	reqBody["messages"] = anthropicMessages

	// Add tools if available (OAuth filtering happens in convertToolsToAnthropic)
	if len(tools) > 0 {
		ccTools := a.convertToolsToAnthropic(tools)
		if len(ccTools) > 0 {
			reqBody["tools"] = ccTools
		}
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers - OAuth requires specific headers to mimic Claude Code
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	if a.isOAuth {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
		httpReq.Header.Set("accept", "text/event-stream")
		httpReq.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14")
		httpReq.Header.Set("anthropic-dangerous-direct-browser-access", "true")
		httpReq.Header.Set("user-agent", "claude-cli/2.1.2 (external, cli)")
		httpReq.Header.Set("x-app", "cli")
	} else {
		httpReq.Header.Set("x-api-key", a.apiKey)
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	log.Printf("[Anthropic] Streaming request: model=%s, isOAuth=%v", modelToUse, a.isOAuth)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(bodyBytes))
	}

	// Parse SSE stream
	return a.parseSSEStream(resp.Body, onDelta)
}

// parseSSEStream parses Server-Sent Events from Anthropic's streaming API
func (a *AnthropicProvider) parseSSEStream(body io.Reader, onDelta StreamCallback) (*GenerateResponse, error) {
	scanner := bufio.NewScanner(body)

	var contentBuilder strings.Builder
	var toolCalls []ToolCall
	var currentToolCall *ToolCall
	var currentToolInput strings.Builder
	var usage Usage

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "event: <event_type>" followed by "data: <json>"
		if strings.HasPrefix(line, "event:") {
			// Event type line - we'll process the data line next
			continue
		}

		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimSpace(data)

		if data == "" || data == "[DONE]" {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			log.Printf("[Streaming] Failed to parse event: %v", err)
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "message_start":
			// Extract input token usage from the message_start event.
			// Anthropic sends input_tokens here; output_tokens come in message_delta.
			if msg, ok := event["message"].(map[string]interface{}); ok {
				if u, ok := msg["usage"].(map[string]interface{}); ok {
					usage.PromptTokens = int(getFloat64(u, "input_tokens"))
				}
			}

		case "content_block_start":
			// New content block starting
			if cb, ok := event["content_block"].(map[string]interface{}); ok {
				if cbType, _ := cb["type"].(string); cbType == "tool_use" {
					// Starting a tool call
					currentToolCall = &ToolCall{
						ID:   cb["id"].(string),
						Name: cb["name"].(string),
					}
					currentToolInput.Reset()
				}
			}

		case "content_block_delta":
			// Content delta
			if delta, ok := event["delta"].(map[string]interface{}); ok {
				deltaType, _ := delta["type"].(string)

				switch deltaType {
				case "text_delta":
					// Text content
					if text, ok := delta["text"].(string); ok {
						contentBuilder.WriteString(text)
						if onDelta != nil {
							onDelta(text, false)
						}
					}

				case "input_json_delta":
					// Tool input JSON (partial)
					if partialJSON, ok := delta["partial_json"].(string); ok {
						currentToolInput.WriteString(partialJSON)
					}
				}
			}

		case "content_block_stop":
			// Content block finished
			if currentToolCall != nil {
				// Parse accumulated tool input
				var args map[string]interface{}
				if err := json.Unmarshal([]byte(currentToolInput.String()), &args); err == nil {
					currentToolCall.Args = args
				}
				toolCalls = append(toolCalls, *currentToolCall)
				currentToolCall = nil
			}

		case "message_delta":
			// Message-level delta (usually contains stop_reason and usage)
			if delta, ok := event["delta"].(map[string]interface{}); ok {
				if stopReason, ok := delta["stop_reason"].(string); ok {
					log.Printf("[Streaming] Stop reason: %s", stopReason)
				}
			}
			// Anthropic sends output_tokens in message_delta usage
			if u, ok := event["usage"].(map[string]interface{}); ok {
				if ot := int(getFloat64(u, "output_tokens")); ot > 0 {
					usage.CompletionTokens = ot
				}
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

		case "message_stop":
			// Stream complete
			if onDelta != nil {
				onDelta("", true)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading stream: %w", err)
	}

	// Ensure TotalTokens is computed even if message_delta was missed
	if usage.TotalTokens == 0 && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	return &GenerateResponse{
		Content:   contentBuilder.String(),
		ToolCalls: toolCalls,
		Usage:     usage,
	}, nil
}

func getFloat64(m map[string]interface{}, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}
