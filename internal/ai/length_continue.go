package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// maxLengthAutoContinues bounds the auto-continue loop (bd-1k3o). It is the
// single budget for every round trip: the router's first call and each
// tool-loop round (internal/tools/execution.go roundTrip) both go through
// ContinueLengthTruncated (conduit-31jg.51).
const maxLengthAutoContinues = 2

// lengthTruncatedMarker is appended when a final is still truncated after
// the continue budget is spent.
const lengthTruncatedMarker = "\n\n_(truncated at max_tokens — ask me to continue if this cuts off)_"

// ContinueLengthTruncated applies the bd-1k3o length-truncation guard to a
// round trip's response. It is shared by the router's first round trip and
// every tool-loop round (ExecutionEngine.roundTrip); the tool loop no longer
// has an auto-continue loop of its own (conduit-31jg.51). History:
// conduit-31jg.11 — the guard originally ran only inside the tool loop after
// tool execution, so a turn whose FIRST reply hit max_tokens was delivered
// truncated as if complete.
//
// While resp.FinishReason == "length" and there are no tool calls, the
// fragment is appended to req.Messages as an assistant turn followed by a
// user "continue" turn and generation resumes (at most
// maxLengthAutoContinues times). req.Messages is mutated on purpose, so a
// continuation that ends in tool calls hands HandleToolCallFlow a history
// that already contains the earlier fragments.
//
// Content: when the final response has no tool calls, the fragments are
// concatenated into its Content (the user-visible answer). When it has tool
// calls, Content stays as the last fragment only — earlier fragments are
// already in req.Messages, and repeating them would duplicate history.
// A continuation error is non-fatal: the fragments gathered so far are
// delivered with the truncation marker.
func ContinueLengthTruncated(ctx context.Context, provider Provider, req *GenerateRequest, resp *GenerateResponse, label string) *GenerateResponse {
	if resp == nil || resp.FinishReason != "length" || len(resp.ToolCalls) > 0 {
		return resp
	}

	// conduit-31jg.15: every continuation is billed; the returned response
	// carries the sum (and total.ContextTokens tracks the last call).
	var total Usage
	total.Add(resp.Usage)
	defer func() { resp.Usage = total }()

	var fragments []string
	for cont := 0; resp.FinishReason == "length" && len(resp.ToolCalls) == 0 && cont < maxLengthAutoContinues; cont++ {
		if strings.TrimSpace(resp.Content) == "" {
			// Nothing to anchor a continuation on (an empty assistant turn
			// is rejected by the Messages API). Leave it to the empty guard.
			break
		}
		log.Printf("[Router] (%s) length-truncated response (max_tokens hit) — auto-continue %d/%d (conduit-31jg.11, bd-1k3o)",
			label, cont+1, maxLengthAutoContinues)
		fragments = append(fragments, resp.Content)
		req.Messages = append(req.Messages,
			ChatMessage{Role: "assistant", Content: resp.Content},
			ChatMessage{Role: "user", Content: "continue"},
		)
		start := time.Now()
		contResp, contErr := provider.GenerateResponse(ctx, req)
		if contErr != nil {
			log.Printf("[Router] (%s) auto-continue failed: %v — delivering truncated content (conduit-31jg.11)", label, contErr)
			// Undo the continue prompt: the response we deliver is the
			// fragment itself, so history must not claim we asked for more.
			req.Messages = req.Messages[:len(req.Messages)-2]
			fragments = fragments[:len(fragments)-1]
			break
		}
		contResp, contErr = GuardEmptyResponse(ctx, provider, req, contResp, contErr, fmt.Sprintf("%s-continue%d", label, cont))
		if contErr != nil || contResp == nil {
			req.Messages = req.Messages[:len(req.Messages)-2]
			fragments = fragments[:len(fragments)-1]
			break
		}
		total.Add(contResp.Usage)
		log.Printf("[RoundTrip] phase=%s-continue continue=%d model=%q duration=%s prompt_tokens=%d completion_tokens=%d content_bytes=%d tool_calls=%d finish_reason=%q",
			label, cont+1, req.Model, time.Since(start).Round(time.Millisecond),
			contResp.Usage.PromptTokens, contResp.Usage.CompletionTokens,
			len(contResp.Content), len(contResp.ToolCalls), contResp.FinishReason)
		resp = contResp
	}

	if len(resp.ToolCalls) > 0 {
		return resp
	}
	if len(fragments) > 0 {
		fragments = append(fragments, resp.Content)
		resp.Content = strings.Join(fragments, "")
	}
	if resp.FinishReason == "length" {
		log.Printf("[Router] (%s) response still length-truncated — delivering with marker (conduit-31jg.11)", label)
		resp.Content += lengthTruncatedMarker
	}
	return resp
}
