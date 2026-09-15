package ai

import (
	"context"
	"fmt"
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

// EmptyFailoverRouter is the optional hook the empty guard uses for one final
// cross-model attempt after the same-model retry also dies (conduit-1z0g).
//
// Root cause (2026-09-14 RCA): z.ai returns HTTP 200 with an EMPTY completion
// payload under load (prompt_tokens=0, duration 2-4.5min). Retrying the same
// model seconds later hits the same sick backend and dies identically — the
// only cure is a different provider. Implementations must honor the bd-27ud
// rule: the fallback model goes to ITS OWN provider, and must return
// ok=false if the resolved provider equals failedProvider (failover to the
// same backend is not failover).
type EmptyFailoverRouter interface {
	ResolveEmptyFailover(failedProvider string) (fallbackModel string, provider Provider, ok bool)
}

// emptyFailoverRouter is the package-level failover source, injected once at
// gateway startup via SetEmptyFailoverRouter. Nil = failover disabled (guard
// degrades to conduit-18vj behavior: same-model retry, then visible fallback).
var emptyFailoverRouter EmptyFailoverRouter

// SetEmptyFailoverRouter wires the failover source. Call once at startup,
// after the AI router is constructed.
func SetEmptyFailoverRouter(r EmptyFailoverRouter) {
	emptyFailoverRouter = r
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

	// conduit-1z0g: same-model retry died too. One final cross-model attempt —
	// z.ai empties are provider-side (HTTP 200, empty payload), so the only
	// cure is a different backend. No failover source wired, or the resolved
	// provider is the same one that just failed twice → visible fallback.
	if emptyFailoverRouter != nil {
		failoverResp, failoverErr := emptyFailoverAttempt(ctx, emptyFailoverRouter, provider, req, label)
		if failoverErr == nil && failoverResp != nil && !IsEmptyModelResponse(failoverResp) {
			return failoverResp, nil
		}
		log.Printf("[EmptyGuard] (%s) failover also empty/failed — delivering visible fallback (conduit-18vj)", label)
		return &GenerateResponse{
			Content: EmptyResponseFallbackContent(),
		}, nil
	}

	log.Printf("[EmptyGuard] (%s) retry also empty/failed — delivering visible fallback (conduit-18vj)", label)
	return &GenerateResponse{
		Content: EmptyResponseFallbackContent(),
	}, nil
}

// emptyFailoverAttempt resolves and executes the cross-model failover attempt
// for the empty guard (conduit-1z0g). Returns the response and error from the
// failover provider. Called only after the same-model retry also returned
// empty/failed. Enforces the same-provider refusal inside the resolver's
// contract (bd-27ud: fallback model goes to its OWN provider).
func emptyFailoverAttempt(
	ctx context.Context,
	router EmptyFailoverRouter,
	failedProvider Provider,
	req *GenerateRequest,
	label string,
) (*GenerateResponse, error) {
	fallbackModel, failoverProvider, ok := router.ResolveEmptyFailover(failedProvider.Name())
	if !ok || failoverProvider == nil {
		log.Printf("[EmptyGuard] (%s) no failover route for provider %q — skipping cross-model attempt (conduit-1z0g)", label, failedProvider.Name())
		return nil, fmt.Errorf("no empty-failover route for provider %q", failedProvider.Name())
	}
	if failoverProvider.Name() == failedProvider.Name() {
		log.Printf("[EmptyGuard] (%s) failover resolved to the FAILED provider %q — refusing (conduit-1z0g)", label, failedProvider.Name())
		return nil, fmt.Errorf("empty-failover resolved to the same provider %q", failedProvider.Name())
	}

	failoverReq := *req // shallow copy — Messages/Tools shared, model differs
	originalModel := req.Model
	failoverReq.Model = fallbackModel

	log.Printf("[EmptyGuard] (%s) failover %q -> %q on provider %q (conduit-1z0g)",
		label, originalModel, fallbackModel, failoverProvider.Name())
	start := time.Now()
	resp, err := failoverProvider.GenerateResponse(ctx, &failoverReq)
	log.Printf("[EmptyGuard] (%s) failover completed in %s: empty=%v err=%v (conduit-1z0g)",
		label, time.Since(start).Round(time.Millisecond), IsEmptyModelResponse(resp), err)
	return resp, err
}
