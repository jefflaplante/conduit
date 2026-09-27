package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	"conduit/internal/models"
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
// Stream: true. The previous hand-rolled request hardcoded
// https://api.anthropic.com and max_tokens=16000, skipped cache breakpoints
// and OAuth refresh, read a.apiKey without oauthMu, and kept only
// messages[0] as the system prompt (later system messages were dropped).
func (a *AnthropicProvider) GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error) {
	if err := a.refreshOAuthToken(); err != nil {
		return nil, fmt.Errorf("failed to refresh OAuth token: %w", err)
	}

	body, modelToUse := a.buildMessagesRequest(req)
	body.Stream = true
	payload, err := marshalMessagesRequest(body)
	if err != nil {
		return nil, err
	}

	log.Printf("[Anthropic] Streaming request: model=%s, isOAuth=%v", modelToUse, a.isOAuth)

	// conduit-31jg.46: retries retryable HTTP statuses and mid-stream
	// overloaded/rate-limit errors, the latter only before any text has
	// been emitted to onDelta.
	return a.streamMessagesWithRetry(ctx, payload, onDelta)
}

// parseSSEStream parses Server-Sent Events from Anthropic's streaming API.
// Events decode into models.StreamEvent, which treats a field of the wrong
// JSON kind as absent: a malformed event is logged and skipped, never a
// panic (conduit-31jg.12, typed since conduit-31jg.36).
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

		var event models.StreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			log.Printf("[Streaming] Failed to parse event: %v", err)
			continue
		}

		switch event.Type {
		case models.EventMessageStart:
			// input_tokens and the cache counters arrive here; output_tokens
			// (cumulative) arrive in message_delta. conduit-31jg.12: cache
			// counts were previously dropped on the streaming path.
			if event.Message != nil && event.Message.Usage != nil {
				mergeAnthropicStreamUsage(&usage, event.Message.Usage)
			}

		case models.EventContentBlockStart:
			cb := event.ContentBlock
			if cb == nil {
				log.Printf("[Streaming] content_block_start without content_block — skipped (conduit-31jg.12)")
				continue
			}
			lastBlockType = cb.Type
			if cb.Type == models.BlockToolUse {
				if !cb.HasID() || !cb.HasName() || cb.ID == "" || cb.Name == "" {
					// conduit-31jg.12: was an unchecked cb["id"].(string) — a
					// malformed block panicked the gateway goroutine.
					log.Printf("[Streaming] malformed tool_use content_block_start (%s) — skipped", event.RawContentBlock)
					currentToolCall = nil
					continue
				}
				currentToolCall = &ToolCall{ID: cb.ID, Name: cb.Name}
				currentToolInput.Reset()
			}

		case models.EventContentBlockDelta:
			delta := event.Delta
			if delta == nil {
				continue
			}
			switch delta.Type {
			case models.DeltaText:
				if delta.HasText() {
					contentBuilder.WriteString(delta.Text)
					if onDelta != nil {
						onDelta(delta.Text, false)
					}
				}
			case models.DeltaInputJSON:
				if delta.HasPartialJSON() && currentToolCall != nil {
					currentToolInput.WriteString(delta.PartialJSON)
				}
			}

		case models.EventContentBlockStop:
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

		case models.EventMessageDelta:
			if delta := event.Delta; delta != nil && delta.HasStopReason() {
				stopReason = delta.StopReason
				if VerboseLogging {
					log.Printf("[Streaming] Stop reason: %s", stopReason)
				}
			}
			if event.Usage != nil {
				mergeAnthropicStreamUsage(&usage, event.Usage)
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

		case models.EventMessageStop:
			if onDelta != nil {
				onDelta("", true)
			}

		case models.EventError:
			// conduit-31jg.12: mid-stream errors (overloaded_error,
			// api_error, ...) used to be ignored, returning the partial text
			// as a successful response. Surface them — with the error type in
			// the message so ClassifyError / the router's retry and fallback
			// logic can act on it.
			errType, errMsg := "unknown_error", ""
			if e := event.Error; e != nil {
				if e.Type != "" {
					errType = e.Type
				}
				errMsg = e.Message
			}
			log.Printf("[Streaming] error event mid-stream: %s: %s", errType, errMsg)
			// conduit-31jg.46: typed so retry logic can read the type.
			return partial(), &anthropicStreamError{Type: errType, Message: errMsg}

		case models.EventPing:
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
	applyAnthropicStopReason(result, stopReason, lastBlockType == models.BlockToolUse)
	return result, nil
}

// mergeAnthropicStreamUsage folds a streaming usage object (from
// message_start or message_delta) into usage. Only non-zero counters
// overwrite, since message_delta usage is cumulative and may omit
// input/cache counters. conduit-31jg.12.
func mergeAnthropicStreamUsage(usage *Usage, u *models.Usage) {
	if u.InputTokens > 0 {
		usage.PromptTokens = u.InputTokens
	}
	if u.OutputTokens > 0 {
		usage.CompletionTokens = u.OutputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		usage.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 {
		usage.CacheReadInputTokens = u.CacheReadInputTokens
	}
}
