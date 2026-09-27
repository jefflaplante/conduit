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

	// conduit-31jg.15: the discarded empty attempts were billed too; the
	// response returned carries the sum of every attempt made here.
	var spent Usage
	if resp != nil {
		spent.Add(resp.Usage)
	}
	visibleFallback := func(why string) (*GenerateResponse, error) {
		log.Printf("[EmptyGuard] (%s) %s — delivering visible fallback (conduit-18vj)", label, why)
		return &GenerateResponse{Content: EmptyResponseFallbackContent(), Usage: spent}, nil
	}

	// conduit-10ip: the retry and failover share the caller's deadline (the
	// 300s sub-agent chain). A dead ctx cannot be recovered — don't launch
	// doomed calls.
	if ctx.Err() != nil {
		return visibleFallback(fmt.Sprintf("raw-empty response but ctx already done (%v), no retry/failover (conduit-10ip)", ctx.Err()))
	}

	// conduit-10ip: under a deadline, whether a failover route exists decides
	// how much of the budget the same-model retry may use: with one, the
	// retry stops recoveryFailoverReserve short of the deadline (or is
	// skipped when that leaves it too little), so the failover — the better
	// bet against a sick backend — always gets its slice.
	var failover *emptyFailoverRoute
	resolved := false
	resolveFailover := func() *emptyFailoverRoute {
		if !resolved {
			resolved = true
			if emptyFailoverRouter != nil {
				failover = resolveEmptyFailoverRoute(emptyFailoverRouter, provider, req, label)
			}
		}
		return failover
	}
	reserve := time.Duration(0)
	if _, hasDeadline := ctx.Deadline(); hasDeadline && resolveFailover() != nil {
		reserve = recoveryFailoverReserve
	}

	retryCtx, cancelRetry, retryOK := retryBudget(ctx, reserve)
	if retryOK {
		log.Printf("[EmptyGuard] (%s) raw-empty response: 0 content bytes, %d tool calls — retrying once (conduit-18vj)",
			label, len(resp.ToolCalls))
		retryStart := time.Now()
		retryResp, retryErr := provider.GenerateResponse(retryCtx, req)
		cancelRetry()
		log.Printf("[EmptyGuard] (%s) retry completed in %s: empty=%v err=%v",
			label, time.Since(retryStart).Round(time.Millisecond), IsEmptyModelResponse(retryResp), retryErr)

		if retryErr == nil && !IsEmptyModelResponse(retryResp) {
			return withSpentUsage(retryResp, spent), nil
		}
		if retryErr == nil && retryResp != nil {
			spent.Add(retryResp.Usage)
		}
	} else {
		cancelRetry()
		left, _ := timeLeft(ctx)
		log.Printf("[EmptyGuard] (%s) raw-empty response with %s left before the deadline — skipping the same-model retry, keeping %s for failover (conduit-10ip)",
			label, left.Round(time.Millisecond), reserve)
	}

	// conduit-1z0g: same-model retry died too (or was skipped). One final
	// cross-model attempt — z.ai empties are provider-side (HTTP 200, empty
	// payload), so the only cure is a different backend. No failover source
	// wired, or the resolved provider is the same one that just failed →
	// visible fallback.
	if emptyFailoverRouter == nil {
		return visibleFallback("retry also empty/failed")
	}
	if route := resolveFailover(); route != nil {
		// conduit-10ip: the reserve above guarantees this slice unless the
		// caller's deadline was already short; never start a call that
		// cannot finish.
		if !hasAttemptBudget(ctx) {
			left, _ := timeLeft(ctx)
			return visibleFallback(fmt.Sprintf("only %s left before the deadline, failover skipped (conduit-10ip)", left.Round(time.Millisecond)))
		}
		failoverResp, failoverErr := route.attempt(ctx, req, label)
		if failoverErr == nil && failoverResp != nil && !IsEmptyModelResponse(failoverResp) {
			return withSpentUsage(failoverResp, spent), nil
		}
		if failoverErr == nil && failoverResp != nil {
			spent.Add(failoverResp.Usage)
		}
	}
	return visibleFallback("failover also empty/failed")
}

// withSpentUsage returns a copy of resp whose Usage also includes usage
// already spent on discarded attempts (conduit-31jg.15). The provider's
// response object is not mutated.
func withSpentUsage(resp *GenerateResponse, spent Usage) *GenerateResponse {
	out := *resp
	total := spent
	total.Add(resp.Usage)
	out.Usage = total
	return &out
}

// emptyFailoverRoute is a validated cross-model failover target for the
// empty guard (conduit-1z0g).
type emptyFailoverRoute struct {
	model    string
	provider Provider
}

// resolveEmptyFailoverRoute resolves the failover target for failedProvider,
// or nil when there is none. conduit-15gt refines the refusal to
// same-provider-AND-same-model: a different model on the failed provider's
// backend is real failover for model-specific failures (glm-5.3-flash
// reasoning exhaustion). When the fallback model cannot be verified as
// different, we fail closed. conduit-10ip split resolution from the call so
// the guard can budget the retry around whether a failover exists.
func resolveEmptyFailoverRoute(router EmptyFailoverRouter, failedProvider Provider, req *GenerateRequest, label string) *emptyFailoverRoute {
	fallbackModel, failoverProvider, ok := router.ResolveEmptyFailover(failedProvider.Name())
	if !ok || failoverProvider == nil {
		log.Printf("[EmptyGuard] (%s) no failover route for provider %q — skipping cross-model attempt (conduit-1z0g)", label, failedProvider.Name())
		return nil
	}
	if failoverProvider.Name() == failedProvider.Name() {
		// conduit-15gt: same backend is meaningful failover only when the
		// model differs. Compare raw and prefix-stripped forms; an empty
		// fallback model is unverifiable — fail closed.
		strippedFallback := stripProviderPrefix(fallbackModel)
		strippedReq := stripProviderPrefix(req.Model)
		if fallbackModel == "" || fallbackModel == req.Model || strippedFallback == strippedReq {
			log.Printf("[EmptyGuard] (%s) failover resolved to the SAME model %q on the failed provider %q — refusing (conduit-15gt)", label, fallbackModel, failedProvider.Name())
			return nil
		}
		log.Printf("[EmptyGuard] (%s) same provider %q, different model %q — proceeding (conduit-15gt)", label, failedProvider.Name(), fallbackModel)
	}
	return &emptyFailoverRoute{model: fallbackModel, provider: failoverProvider}
}

// attempt executes the failover call on a copy of req carrying the route's
// model. Called only after the same-model retry returned empty/failed or
// was skipped for lack of budget.
func (fo *emptyFailoverRoute) attempt(ctx context.Context, req *GenerateRequest, label string) (*GenerateResponse, error) {
	failoverReq := *req // shallow copy — Messages/Tools shared, model differs
	failoverReq.Model = fo.model

	log.Printf("[EmptyGuard] (%s) failover %q -> %q on provider %q (conduit-1z0g)",
		label, req.Model, fo.model, fo.provider.Name())
	start := time.Now()
	resp, err := fo.provider.GenerateResponse(ctx, &failoverReq)
	log.Printf("[EmptyGuard] (%s) failover completed in %s: empty=%v err=%v (conduit-1z0g)",
		label, time.Since(start).Round(time.Millisecond), IsEmptyModelResponse(resp), err)
	return resp, err
}
