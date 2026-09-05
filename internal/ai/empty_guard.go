package ai

import (
	"context"
	"log"
	"strings"
	"time"
)

// Empty-response guard (conduit-18vj).
//
// Root cause of dead turns (2026-09-03 night): the model occasionally returns a
// response with NO content and NO tool calls. That "raw empty" response was
// treated as a valid turn completion, and the gateway suppressed empty content
// — so the user received nothing while the chain logged a normal END.
//
// Deliberate silence (NO_REPLY / HEARTBEAT_OK tokens) is handled elsewhere and
// remains correct. This guard targets raw empties only.

// IsEmptyModelResponse reports whether a raw model response carries no content
// and no tool calls (i.e. nothing deliverable and nothing to do).
func IsEmptyModelResponse(resp *GenerateResponse) bool {
	if resp == nil {
		return true
	}
	if len(resp.ToolCalls) > 0 {
		return false
	}
	return strings.TrimSpace(resp.Content) == ""
}

// emptyResponseFallback is the user-visible terminal message delivered when a
// round trip returns empty even after a retry. Every turn must end with
// SOMETHING (conduit-18vj guarantee).
const emptyResponseFallback = "I hit an empty response from the model on that turn " +
	"(retried once, still nothing). Your message wasn't lost — please nudge me again " +
	"and I'll pick it up."

// EmptyResponseFallbackContent returns the terminal fallback text delivered when
// both the original round trip and its one retry return raw-empty responses.
func EmptyResponseFallbackContent() string {
	return emptyResponseFallback
}

// IsEmptyResponseFallback reports whether content is the locally-generated
// empty-guard terminal fallback (bd-1k3o). Sub-agent completion paths use this
// to route such finals to the failure/wake path: the fallback text is not a
// model answer, and treating it as one caused silent sub-agent deaths
// (2026-09-04 RCA — sessions "Completed" while delivering nothing of value).
func IsEmptyResponseFallback(content string) bool {
	return strings.TrimSpace(content) == emptyResponseFallback
}

// GuardEmptyResponse inspects a raw model response. On raw-empty it retries the
// generation ONCE, then falls back to a user-visible terminal message. It never
// returns an empty response: callers get content or an error, never silence.
//
// label tags the journal lines (e.g. "initial", "depth3") so dead turns are
// diagnosable from logs alone (conduit-1z6d instrumentation companion).
func GuardEmptyResponse(
	ctx context.Context,
	provider Provider,
	req *GenerateRequest,
	resp *GenerateResponse,
	genErr error,
	label string,
) (*GenerateResponse, error) {
	if !IsEmptyModelResponse(resp) {
		return resp, genErr
	}

	// Generation itself errored — that's the existing retry/fallback machinery's
	// job, not this guard's.
	if genErr != nil {
		return resp, genErr
	}

	log.Printf("[EmptyGuard] (%s) raw-empty response: 0 content bytes, %d tool calls — retrying once (conduit-18vj)",
		label, len(resp.ToolCalls))

	retryStart := time.Now()
	retryResp, retryErr := provider.GenerateResponse(ctx, req)
	log.Printf("[EmptyGuard] (%s) retry completed in %s: empty=%v err=%v",
		label, time.Since(retryStart).Round(time.Millisecond), IsEmptyModelResponse(retryResp), retryErr)

	if retryErr == nil && !IsEmptyModelResponse(retryResp) {
		return retryResp, nil
	}

	log.Printf("[EmptyGuard] (%s) retry also empty/failed — delivering visible fallback (conduit-18vj)", label)
	return &GenerateResponse{
		Content: EmptyResponseFallbackContent(),
	}, nil
}
