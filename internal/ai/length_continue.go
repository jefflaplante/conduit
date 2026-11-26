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

// autoContinuePrompt is the synthetic user turn asking the model to resume a
// length-truncated reply; the message carries Injected=true.
const autoContinuePrompt = "continue"

// lengthTruncatedMarker is appended when a final is still truncated after
// the continue budget is spent.
const lengthTruncatedMarker = "\n\n_(truncated at max_tokens — ask me to continue if this cuts off)_"

// fragmentModel reports the model that produced the fragment being continued
// (sd-glm54-stallcap): resp.ServedBy when stamped (EmptyGuard cross-model
// failover), req.Model otherwise — i.e. unchanged behavior for the common
// in-process path.
func fragmentModel(resp *GenerateResponse, req *GenerateRequest) string {
	if resp != nil && resp.ServedBy != "" {
		return resp.ServedBy
	}
	if req == nil {
		return ""
	}
	return req.Model
}

// continuationRequest derives the next continuation's request: a copy of req
// carrying the fragment's model. req itself is never mutated (its Model is
// the caller's bookkeeping anchor for later rounds and log lines).
func continuationRequest(req *GenerateRequest, resp *GenerateResponse) *GenerateRequest {
	if req == nil {
		return nil
	}
	contReq := *req // shallow copy — Messages/Tools shared
	contReq.Model = fragmentModel(resp, req)
	return &contReq
}

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
//
// Sticky continuation model (sd-glm54-stallcap): each continuation goes to
// the model that produced the FRAGMENT being continued — resp.ServedBy when
// set (the EmptyGuard's cross-model failover produced it), req.Model
// otherwise — instead of always the request's original model. A 5.3-produced
// fragment handed back to 5.4 re-ignites the glm-5.4 stall spiral. req.Model
// is never mutated; each iteration re-derives the model from the latest
// response, so a mid-loop failover re-anchors the rest of the loop.
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
		log.Printf("[Router] (%s) length-truncated response (max_tokens hit) — auto-continue %d/%d on model %q (conduit-31jg.11, bd-1k3o, sd-glm54-stallcap)",
			label, cont+1, maxLengthAutoContinues, fragmentModel(resp, req))
		fragments = append(fragments, resp.Content)
		req.Messages = append(req.Messages,
			ChatMessage{Role: "assistant", Content: resp.Content},
			ChatMessage{Role: "user", Content: autoContinuePrompt, Injected: true},
		)
		start := time.Now()
		contReq := continuationRequest(req, resp)
		contResp, contErr := provider.GenerateResponse(withCallPhase(ctx, "continue"), contReq) // conduit-2lzv
		if contErr != nil {
			log.Printf("[Router] (%s) auto-continue failed: %v — delivering truncated content (conduit-31jg.11)", label, contErr)
			// Undo the continue prompt: the response we deliver is the
			// fragment itself, so history must not claim we asked for more.
			req.Messages = req.Messages[:len(req.Messages)-2]
			fragments = fragments[:len(fragments)-1]
			break
		}
		// The guard recovers a raw-empty continuation, possibly via a
		// cross-model failover whose response carries ServedBy — pass contReq
		// so the failover attempt keeps the sticky model (and doesn't snap
		// back to req.Model for the guard's own same-model-refusal check).
		contResp, contErr = GuardEmptyResponse(ctx, provider, contReq, contResp, contErr, fmt.Sprintf("%s-continue%d", label, cont))
		if contErr != nil || contResp == nil {
			req.Messages = req.Messages[:len(req.Messages)-2]
			fragments = fragments[:len(fragments)-1]
			break
		}
		total.Add(contResp.Usage)
		log.Printf("[RoundTrip] phase=%s-continue continue=%d model=%q duration=%s prompt_tokens=%d completion_tokens=%d content_bytes=%d tool_calls=%d finish_reason=%q",
			label, cont+1, contReq.Model, time.Since(start).Round(time.Millisecond),
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
