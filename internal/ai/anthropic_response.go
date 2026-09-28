package ai

import (
	"log"
	"strings"

	"conduit/internal/models"
)

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
		// (convertToolsToAnthropic) and the response parser does not keep
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
func lastContentBlockType(resp *models.MessagesResponse) string {
	if len(resp.Content) == 0 {
		return ""
	}
	return resp.Content[len(resp.Content)-1].Type
}

// parseAnthropicContent extracts content and tool calls from an Anthropic
// response. Text blocks are joined with "\n", tool_use blocks become tool
// calls, and every other block type (thinking, server tool blocks, ...) is
// ignored.
func (a *AnthropicProvider) parseAnthropicContent(resp *models.MessagesResponse) (string, []ToolCall) {
	var content strings.Builder
	var toolCalls []ToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case models.BlockText:
			if block.HasText() {
				if content.Len() > 0 {
					content.WriteString("\n")
				}
				content.WriteString(block.Text)
			}
		case models.BlockToolUse:
			if toolCall := a.parseAnthropicToolCall(block); toolCall != nil {
				toolCalls = append(toolCalls, *toolCall)
			}
		}
	}

	return content.String(), toolCalls
}

// parseAnthropicToolCall extracts a tool call from an Anthropic tool_use
// block: id and name must be strings and input a JSON object, otherwise the
// block is skipped.
func (a *AnthropicProvider) parseAnthropicToolCall(block models.ResponseBlock) *ToolCall {
	if !block.HasID() || !block.HasName() || block.Input == nil {
		return nil
	}
	return &ToolCall{
		ID:   block.ID,
		Name: block.Name,
		Args: block.Input,
	}
}

// parseAnthropicUsage converts a response usage object (nil when absent).
func (a *AnthropicProvider) parseAnthropicUsage(u *models.Usage) Usage {
	var usage Usage
	if u == nil {
		return usage
	}
	usage.PromptTokens = u.InputTokens
	usage.CompletionTokens = u.OutputTokens
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	usage.CacheCreationInputTokens = u.CacheCreationInputTokens
	usage.CacheReadInputTokens = u.CacheReadInputTokens
	return usage
}
