package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// VerboseLogging controls whether debug-level AI messages appear in the journal.
// Set from gateway.go using the config value: ai.VerboseLogging = cfg.Debug.VerboseLogging
var VerboseLogging bool

// StreamCallback is called with text deltas during streaming
type StreamCallback func(delta string, done bool)

// StreamingResponse holds accumulated streaming data
type StreamingResponse struct {
	Content   string
	ToolCalls []ToolCall
	Usage     *Usage
}

// maxSSELineBytes bounds a single SSE line. bufio.Scanner's 64KiB default is
// too small for large content_block_start / error payloads.
const maxSSELineBytes = 4 << 20

// GenerateResponseStreaming implements StreamingProvider for the Anthropic
// provider.
//
// conduit-31jg.12: the request is built by the SAME code path as
// GenerateResponse (buildMessagesRequest + newMessagesHTTPRequest) plus
// "stream": true. The previous hand-rolled request hardcoded
// https://api.anthropic.com and max_tokens=16000, skipped cache breakpoints
// and OAuth refresh, read a.apiKey without oauthMu, and kept only
// messages[0] as the system prompt (later system messages were dropped).
func (a *AnthropicProvider) GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error) {
	if err := a.refreshOAuthToken(); err != nil {
		return nil, fmt.Errorf("failed to refresh OAuth token: %w", err)
	}

	body, modelToUse := a.buildMessagesRequest(req)
	body["stream"] = true

	httpReq, err := a.newMessagesHTTPRequest(ctx, body, true)
	if err != nil {
		return nil, err
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

	return a.parseSSEStream(resp.Body, onDelta)
}

// parseSSEStream parses Server-Sent Events from Anthropic's streaming API.
// All type assertions are comma-ok: a malformed event is logged and skipped,
// never a panic (conduit-31jg.12).
func (a *AnthropicProvider) parseSSEStream(body io.Reader, onDelta StreamCallback) (*GenerateResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)

	var contentBuilder strings.Builder
	var toolCalls []ToolCall
	var currentToolCall *ToolCall
	var currentToolInput strings.Builder
	var usage Usage
	// conduit-31jg.11: stop_reason arrives in message_delta; lastBlockType
	// tells us whether a max_tokens stop severed a tool_use block.
	var stopReason string
	var lastBlockType string

	// partial packages what was received so far for error returns. The
	// Partial flag tells callers the response is incomplete.
	partial := func() *GenerateResponse {
		if contentBuilder.Len() == 0 && len(toolCalls) == 0 {
			return nil
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		return &GenerateResponse{
			Content:   contentBuilder.String(),
			ToolCalls: toolCalls,
			Usage:     usage,
			Partial:   true,
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "event: <event_type>" followed by "data: <json>".
		// The data payload repeats the type, so the event line is skipped.
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
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
			// input_tokens and the cache counters arrive here; output_tokens
			// (cumulative) arrive in message_delta. conduit-31jg.12: cache
			// counts were previously dropped on the streaming path.
			if msg, ok := event["message"].(map[string]interface{}); ok {
				if u, ok := msg["usage"].(map[string]interface{}); ok {
					mergeAnthropicStreamUsage(&usage, u)
				}
			}

		case "content_block_start":
			cb, ok := event["content_block"].(map[string]interface{})
			if !ok {
				log.Printf("[Streaming] content_block_start without content_block — skipped (conduit-31jg.12)")
				continue
			}
			cbType, _ := cb["type"].(string)
			lastBlockType = cbType
			if cbType == "tool_use" {
				id, idOK := cb["id"].(string)
				name, nameOK := cb["name"].(string)
				if !idOK || !nameOK || id == "" || name == "" {
					// conduit-31jg.12: was an unchecked cb["id"].(string) — a
					// malformed block panicked the gateway goroutine.
					log.Printf("[Streaming] malformed tool_use content_block_start (id=%v name=%v) — skipped", cb["id"], cb["name"])
					currentToolCall = nil
					continue
				}
				currentToolCall = &ToolCall{ID: id, Name: name}
				currentToolInput.Reset()
			}

		case "content_block_delta":
			delta, ok := event["delta"].(map[string]interface{})
			if !ok {
				continue
			}
			deltaType, _ := delta["type"].(string)
			switch deltaType {
			case "text_delta":
				if text, ok := delta["text"].(string); ok {
					contentBuilder.WriteString(text)
					if onDelta != nil {
						onDelta(text, false)
					}
				}
			case "input_json_delta":
				if partialJSON, ok := delta["partial_json"].(string); ok && currentToolCall != nil {
					currentToolInput.WriteString(partialJSON)
				}
			}

		case "content_block_stop":
			if currentToolCall != nil {
				raw := currentToolInput.String()
				if strings.TrimSpace(raw) == "" {
					// No-argument tool: match the non-streaming path's {}.
					currentToolCall.Args = map[string]interface{}{}
				} else {
					var args map[string]interface{}
					if err := json.Unmarshal([]byte(raw), &args); err == nil {
						currentToolCall.Args = args
					} else {
						log.Printf("[Streaming] tool_use %q input is not valid JSON (%v) — likely truncated", currentToolCall.Name, err)
					}
				}
				toolCalls = append(toolCalls, *currentToolCall)
				currentToolCall = nil
			}

		case "message_delta":
			if delta, ok := event["delta"].(map[string]interface{}); ok {
				if sr, ok := delta["stop_reason"].(string); ok {
					stopReason = sr
					if VerboseLogging {
						log.Printf("[Streaming] Stop reason: %s", sr)
					}
				}
			}
			if u, ok := event["usage"].(map[string]interface{}); ok {
				mergeAnthropicStreamUsage(&usage, u)
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

		case "message_stop":
			if onDelta != nil {
				onDelta("", true)
			}

		case "error":
			// conduit-31jg.12: mid-stream errors (overloaded_error,
			// api_error, ...) used to be ignored, returning the partial text
			// as a successful response. Surface them — with the error type in
			// the message so ClassifyError / the router's retry and fallback
			// logic can act on it.
			errType, errMsg := "unknown_error", ""
			if e, ok := event["error"].(map[string]interface{}); ok {
				if t, ok := e["type"].(string); ok && t != "" {
					errType = t
				}
				errMsg, _ = e["message"].(string)
			}
			log.Printf("[Streaming] error event mid-stream: %s: %s", errType, errMsg)
			return partial(), fmt.Errorf("anthropic stream error: %s: %s", errType, errMsg)

		case "ping":
			// keepalive
		}
	}

	if err := scanner.Err(); err != nil {
		// Return partial response with the error so callers know what was received.
		return partial(), fmt.Errorf("error reading stream: %w", err)
	}

	// Ensure TotalTokens is computed even if message_delta was missed
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	result := &GenerateResponse{
		Content:   contentBuilder.String(),
		ToolCalls: toolCalls,
		Usage:     usage,
	}
	// conduit-31jg.11: same stop_reason mapping as the non-streaming path.
	// A tool_use severed by max_tokens reached content_block_stop with
	// unparseable partial JSON (Args=nil) and used to be executed anyway.
	applyAnthropicStopReason(result, stopReason, lastBlockType == "tool_use")
	return result, nil
}

// mergeAnthropicStreamUsage folds a streaming usage object (from
// message_start or message_delta) into usage. Only fields present and
// non-zero overwrite, since message_delta usage is cumulative and may omit
// input/cache counters. conduit-31jg.12.
func mergeAnthropicStreamUsage(usage *Usage, u map[string]interface{}) {
	if v := int(getFloat64(u, "input_tokens")); v > 0 {
		usage.PromptTokens = v
	}
	if v := int(getFloat64(u, "output_tokens")); v > 0 {
		usage.CompletionTokens = v
	}
	if v := int(getFloat64(u, "cache_creation_input_tokens")); v > 0 {
		usage.CacheCreationInputTokens = v
	}
	if v := int(getFloat64(u, "cache_read_input_tokens")); v > 0 {
		usage.CacheReadInputTokens = v
	}
}

func getFloat64(m map[string]interface{}, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}
