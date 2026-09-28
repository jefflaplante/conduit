package tools

import (
	"strconv"
	"strings"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools/debuglog"
)

// Debug ring buffer LLM entries (conduit-3kgo). The tool loop records one
// llm_request / llm_response pair per post-tool provider round trip, plus an
// llm_response for the router's initial reply that opened the loop. Entries
// hold metadata and short redacted previews (debuglog.Preview) only — never
// the full prompt or history. Turns that end without a tool call never
// enter the loop and are not recorded here.
//
// No thinking entries are produced: ai.GenerateResponse does not carry the
// provider's thinking/reasoning content (the Anthropic and OpenAI-compatible
// parsers drop those blocks), so there is nothing to record.

// llmModelLabel names the model of a round: the request's model, else the
// provider's name (the provider default applies).
func llmModelLabel(provider ai.Provider, model string) string {
	if model != "" {
		return model
	}
	if provider != nil {
		return provider.Name() + ":default"
	}
	return "default"
}

// recordLLMRequest records an outbound post-tool round trip.
func (e *ExecutionEngine) recordLLMRequest(provider ai.Provider, req *ai.GenerateRequest, depth int) {
	if e.debugBuffer == nil || req == nil {
		return
	}
	meta := map[string]string{
		"depth":    strconv.Itoa(depth),
		"messages": strconv.Itoa(len(req.Messages)),
		"tools":    strconv.Itoa(len(req.Tools)),
	}
	if req.MaxTokens > 0 {
		meta["max_tokens"] = strconv.Itoa(req.MaxTokens)
	}
	if n := len(req.Messages); n > 0 {
		last := req.Messages[n-1]
		meta["last_role"] = last.Role
		meta["last_preview"] = debuglog.Preview(last.Content, debuglog.PreviewLen)
	}
	e.debugBuffer.Add(debuglog.LLMRequest(llmModelLabel(provider, req.Model), meta))
}

// recordLLMResponse records a provider reply (resp) or failure (err).
// phase is "initial" for the router's first reply, "post_tools" otherwise.
// d is the round trip's duration (0 when unknown).
func (e *ExecutionEngine) recordLLMResponse(provider ai.Provider, model, phase string, depth int, resp *ai.GenerateResponse, err error, d time.Duration) {
	if e.debugBuffer == nil {
		return
	}
	meta := map[string]string{
		"phase": phase,
		"depth": strconv.Itoa(depth),
	}
	stop := ""
	if err != nil {
		stop = "error"
	} else if resp != nil {
		stop = resp.StopReason
		if stop == "" {
			stop = resp.FinishReason
		}
		meta["prompt_tokens"] = strconv.Itoa(resp.Usage.PromptTokens)
		meta["completion_tokens"] = strconv.Itoa(resp.Usage.CompletionTokens)
		if resp.Usage.CacheReadInputTokens > 0 {
			meta["cache_read_tokens"] = strconv.Itoa(resp.Usage.CacheReadInputTokens)
		}
		meta["content_bytes"] = strconv.Itoa(len(resp.Content))
		meta["tool_calls"] = strconv.Itoa(len(resp.ToolCalls))
		if len(resp.ToolCalls) > 0 {
			names := make([]string, 0, len(resp.ToolCalls))
			for _, tc := range resp.ToolCalls {
				names = append(names, tc.Name)
			}
			meta["tool_names"] = debuglog.Preview(strings.Join(names, ","), debuglog.PreviewLen)
		}
		if resp.Content != "" {
			meta["content_preview"] = debuglog.Preview(resp.Content, debuglog.PreviewLen)
		}
		if resp.Partial {
			meta["partial"] = "true"
		}
	}
	if stop == "" {
		stop = "unknown"
	}
	entry := debuglog.LLMResponse(llmModelLabel(provider, model), stop, d)
	entry.Meta = meta
	if err != nil {
		entry.Error = debuglog.Preview(err.Error(), 500)
	}
	e.debugBuffer.Add(entry)
}
