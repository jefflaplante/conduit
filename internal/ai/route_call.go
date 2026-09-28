package ai

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// providerRoute pairs a provider with the model string sent to it.
//
// conduit-31jg.18(a): the recovery ladder used to track the provider and the
// model separately — req.Model was switched to the fallback model before the
// fallback call, and when that call timed out the bd-13p retry went to the
// ORIGINAL provider with the fallback model name (the bd-27ud class of bug:
// "z-ai/glm-5.3" sent to Anthropic → 404). Every attempt now takes a route,
// so a provider can only ever receive the model it was paired with.
type providerRoute struct {
	name     string
	provider Provider
	model    string // "" = the provider's configured default
	// handedOff marks a route the turn MOVED to (quota fallback or timeout
	// handoff in callWithRecovery). A guard built on such a route never
	// moves again: at most one route change per turn (conduit-31jg.79).
	handedOff bool
}

// contextWindowForRoute returns the prompt window for a route: the provider's
// configured context_window override, else the window of the route's model
// (or, when the model is empty, of the provider's configured default model).
func (r *Router) contextWindowForRoute(rt providerRoute) int {
	r.mu.RLock()
	meta, ok := r.providerMeta[rt.name]
	r.mu.RUnlock()
	if ok && meta.ContextWindow > 0 {
		return meta.ContextWindow
	}
	model := rt.model
	if model == "" && ok {
		model = meta.DefaultModel
	}
	return ContextWindowForModel(model)
}

// routeModel returns the model a route actually runs: its explicit model,
// else the provider's configured default.
func (r *Router) routeModel(rt providerRoute) string {
	if rt.model != "" {
		return rt.model
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providerMeta[rt.name].DefaultModel
}

// distinctFallbackRoute returns rt's configured fallback route, or nil when
// none resolves or it would send the same model to the same provider
// (z-ai's fallback_model "z-ai/glm-5.3" while already on glm-5.3 — a
// "handoff" to the same backend and model is just a third attempt).
// conduit-1w48.
func (r *Router) distinctFallbackRoute(rt providerRoute) *providerRoute {
	fbModel, fbProvider, ok := r.resolveFallbackRoute(rt.name)
	if !ok {
		return nil
	}
	fb := providerRoute{name: fbProvider.Name(), provider: fbProvider, model: fbModel}
	if fb.name == rt.name &&
		strings.EqualFold(stripProviderPrefix(r.routeModel(fb)), stripProviderPrefix(r.routeModel(rt))) {
		return nil
	}
	return &fb
}

// recoveryOpts tunes callWithRecovery for a call site.
type recoveryOpts struct {
	// phase tags journal lines ("generate", "tool loop", "streaming").
	phase string
	// quotaFallbackNeedsModel keeps the tool-path rule that quota fallback
	// only runs when the request named a model explicitly.
	quotaFallbackNeedsModel bool
	// stream, when non-nil, makes attempts stream through the tracker.
	stream *streamTracker
}

// callWithRecovery runs the first provider call of a turn through the
// router's recovery ladder and returns the response, the route that served
// it (or that failed last), and the latency of the final attempt:
//
//  1. call the primary route;
//  2. bd-6tb/bd-27ud: quota error → the fallback model on ITS OWN provider;
//  3. bd-13p: transient timeout → retry once on the route that timed out
//     (the fallback route if step 2 ran — conduit-31jg.18(a));
//     conduit-1w48: if that retry also times out and step 2 did not run,
//     hand off ONCE to the route's fallback (when it is a different
//     route). Under a deadline the retry leaves the fallback
//     recoveryFailoverReserve, or is skipped when it cannot
//     (deadline_budget.go);
//  4. conduit-31jg.46: streaming overload/rate-limit error AFTER text
//     reached the client (providers only retry before first emission) →
//     one muted retry on the same route.
//
// Each attempt works on a copy of req carrying the route's model, trimmed
// to that route's context window. On success the served copy is written
// back to *req so the tool loop continues with the served model and history.
func (r *Router) callWithRecovery(ctx context.Context, primary providerRoute, req *GenerateRequest, opts recoveryOpts) (*GenerateResponse, providerRoute, int64, error) {
	cur := primary
	var served GenerateRequest
	ctx = beginRecoveryCall(ctx) // conduit-2lzv: call-log phase depth0, attempt counter
	attempt := func(ctx context.Context, rt providerRoute) (*GenerateResponse, int64, error) {
		areq := *req
		obs := &callObs{}
		areq.Model = rt.model
		trimRequestToFitContext(&areq, r.contextWindowForRoute(rt))
		// conduit-38cz: take the slot before the latency clock starts; an
		// abandoned wait makes no call and is neither metered nor logged.
		release, werr := r.acquireProviderSlotObserved(ctx, rt.name, areq.Model, obs)
		if werr != nil {
			return nil, 0, werr
		}
		defer release()
		start := time.Now()
		var resp *GenerateResponse
		var err error
		if opts.stream != nil {
			if sp, ok := rt.provider.(StreamingProvider); ok {
				resp, err = sp.GenerateResponseStreaming(ctx, &areq, obs.stream(opts.stream.callback()))
			} else {
				// Non-streaming fallback route: the final content still
				// reaches the client (the gateway delivers the final text).
				log.Printf("[Router] (%s) provider %q cannot stream — non-streaming attempt, final content will be delivered whole", opts.phase, rt.name)
				resp, err = rt.provider.GenerateResponse(ctx, &areq)
			}
		} else {
			resp, err = rt.provider.GenerateResponse(ctx, &areq)
		}
		latency := time.Since(start).Milliseconds()
		r.meterCall(ctx, rt.name, areq.Model, resp, err, latency, obs) // conduit-31jg.64
		if err == nil {
			served = areq
		}
		return resp, latency, err
	}

	resp, latencyMs, err := attempt(ctx, cur)

	fallbackAllowed := !(opts.quotaFallbackNeedsModel && primary.model == "")
	handedOff := false // at most ONE move to the fallback route per call (conduit-1w48)

	if err != nil && IsQuotaError(err) && fallbackAllowed {
		if fbModel, fbProvider, ok := r.resolveFallbackRoute(primary.name); ok {
			fb := providerRoute{name: fbProvider.Name(), provider: fbProvider, model: fbModel}
			log.Printf("[Router] (%s) quota error on %q model=%q, retrying with fallback model %q on provider %q (bd-27ud)",
				opts.phase, primary.name, primary.model, fb.model, fb.name)
			cur = fb
			cur.handedOff = true
			handedOff = true
			resp, latencyMs, err = attempt(ctx, cur)
			if err == nil {
				log.Printf("[Router] (%s) fallback retry succeeded (bd-27ud): %q -> %q", opts.phase, primary.model, fb.model)
			} else {
				log.Printf("[Router] (%s) fallback retry failed: %v (bd-27ud)", opts.phase, err)
			}
		}
	}

	if err != nil && IsTransientTimeoutError(err) && ctx.Err() == nil {
		// conduit-1w48: a route whose timeout retry is exhausted hands off
		// to its fallback route — once, and only when that is a genuinely
		// different route and the quota step has not already moved there.
		var timeoutFB *providerRoute
		if fallbackAllowed && !handedOff {
			timeoutFB = r.distinctFallbackRoute(cur)
		}
		// conduit-10ip/1w48: under a deadline the same-route retry leaves
		// the fallback a reserved slice, or is skipped when it cannot.
		reserve := time.Duration(0)
		if _, hasDeadline := ctx.Deadline(); hasDeadline && timeoutFB != nil {
			reserve = recoveryFailoverReserve
		}
		retryCtx, cancelRetry, retryOK := retryBudget(ctx, reserve)
		if retryOK {
			// conduit-31jg.18(a): retry the PAIR that timed out.
			log.Printf("[Router] (%s) transient timeout on provider %q model=%q, retrying once (bd-13p)", opts.phase, cur.name, cur.model)
			resp, latencyMs, err = attempt(retryCtx, cur)
			if err == nil {
				log.Printf("[Router] (%s) timeout retry succeeded (bd-13p)", opts.phase)
			} else {
				log.Printf("[Router] (%s) timeout retry failed: %v (bd-13p)", opts.phase, err)
			}
		} else {
			left, _ := timeLeft(ctx)
			log.Printf("[Router] (%s) transient timeout on provider %q model=%q with %s left — skipping same-route retry (conduit-1w48)",
				opts.phase, cur.name, cur.model, left.Round(time.Millisecond))
		}
		cancelRetry()

		if err != nil && IsTransientTimeoutError(err) && timeoutFB != nil {
			if hasAttemptBudget(ctx) {
				log.Printf("[Router] (%s) timeout retries exhausted on provider %q model=%q, handing off to fallback model %q on provider %q (conduit-1w48)",
					opts.phase, cur.name, cur.model, timeoutFB.model, timeoutFB.name)
				cur = *timeoutFB
				cur.handedOff = true
				resp, latencyMs, err = attempt(ctx, cur)
				if err == nil {
					log.Printf("[Router] (%s) timeout fallback succeeded (conduit-1w48)", opts.phase)
				} else {
					log.Printf("[Router] (%s) timeout fallback failed: %v (conduit-1w48)", opts.phase, err)
				}
			} else {
				log.Printf("[Router] (%s) no deadline budget left for the timeout fallback to %q (conduit-1w48)", opts.phase, timeoutFB.name)
			}
		}
	}

	// conduit-31jg.46: providers retry overload/rate-limit errors only while
	// nothing has been streamed (anthropic_retry.go). When text already
	// reached the client, one muted retry on the same route; the final
	// content replaces the partial stream.
	if err != nil && opts.stream != nil && opts.stream.emitted() && IsRetryableOverloadError(err) && ctx.Err() == nil {
		log.Printf("[Router] (%s) overload after %d streamed bytes on provider %q model=%q — one muted retry (conduit-31jg.46)",
			opts.phase, opts.stream.emittedBytes(), cur.name, cur.model)
		resp, latencyMs, err = attempt(ctx, cur)
	}

	if err == nil {
		*req = served
	}
	return resp, cur, latencyMs, err
}

// IsRetryableOverloadError reports whether err is a transient
// capacity/rate-limit failure (HTTP 429/503/529, Anthropic overloaded_error
// or rate_limit_error, including mid-stream error events) that a later retry
// can clear. Quota exhaustion is excluded — it goes to the fallback model.
// conduit-31jg.46.
func IsRetryableOverloadError(err error) bool {
	if err == nil || IsQuotaError(err) {
		return false
	}
	if se, ok := asAnthropicStreamError(err); ok {
		return se.Type == "overloaded_error" || se.Type == "rate_limit_error"
	}
	switch providerStatusCode(err) {
	case 429, 503, 529:
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "overloaded_error") || strings.Contains(msg, "rate_limit_error")
}

// streamTracker wraps a turn's StreamCallback so recovery attempts never
// duplicate text on the client (conduit-31jg.18). Until the
// first delta is emitted, attempts stream normally. Once text has reached
// the client, later attempts run MUTED: their deltas are dropped and the
// turn's final content — which the gateway delivers whole (WS StreamEnd,
// Telegram final edit) — replaces the partial stream.
type streamTracker struct {
	onDelta StreamCallback
	mu      sync.Mutex
	bytes   int
}

func newStreamTracker(onDelta StreamCallback) *streamTracker {
	return &streamTracker{onDelta: onDelta}
}

func (s *streamTracker) emitted() bool { return s.emittedBytes() > 0 }

func (s *streamTracker) emittedBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

// callback returns the StreamCallback for the next attempt.
func (s *streamTracker) callback() StreamCallback {
	if s.onDelta == nil {
		return nil
	}
	if s.emitted() {
		log.Printf("[Router] stream restart: %d bytes already delivered — retry deltas muted (conduit-31jg.18)", s.emittedBytes())
		return func(string, bool) {}
	}
	return func(delta string, done bool) {
		if delta != "" {
			s.mu.Lock()
			s.bytes += len(delta)
			s.mu.Unlock()
		}
		s.onDelta(delta, done)
	}
}

// contextGuardProvider trims every request to its route's context window
// before calling the wrapped provider. conduit-31jg.18(b): the trim ran only
// on the first request of a turn; each tool round appended results with no
// re-check, so 20+ step chains hit "prompt is too long" 400s. The router
// hands this wrapper to the tool loop (HandleToolCallFlow), the empty guard
// and the length auto-continue, so every later provider call is guarded
// without changes to the tool loop itself. Name() passes through.
//
// conduit-31jg.64: it is also the metering point for every call after the
// first (tool-loop depths, EmptyGuard retries and failover, auto-continues);
// callWithRecovery meters the first call's attempts. Together they record
// each provider call to the usage tracker (fuel gauge, TokenWindowTracker)
// exactly once — every attempt below goes through call(), which meters it.
//
// conduit-31jg.68(2) / conduit-31jg.79: it is also the recovery point for
// those calls, mirroring callWithRecovery's ladder:
//
//   - quota error → the route's fallback (own provider, own model, own
//     window) under the same ctx — turn lease and deadline unchanged;
//   - transient timeout → one same-route retry, then ONE handoff to
//     distinctFallbackRoute. Under a deadline the retry stops
//     recoveryFailoverReserve short so the handoff keeps its slice, or is
//     skipped when that leaves it too little (deadline_budget.go).
//
// A successful move is sticky for the rest of the turn, so later rounds
// don't burn a failed call on the sick or exhausted route first. At most
// ONE route change per turn: a guard that already switched, or that was
// built on a route callWithRecovery moved to (providerRoute.handedOff),
// only retries its current route.
//
// Layer ownership (conduit-31jg.79). Errors (timeout, quota) belong to this
// guard; raw-empty responses belong to GuardEmptyResponse. A call the
// EmptyGuard makes (its same-model retry, marked on ctx by
// withEmptyGuardCall) passes through here trimmed, metered and on the
// sticky route, but with NO recovery: the EmptyGuard's own follow-up is the
// cross-route failover, so recovering here too would try the fallback route
// twice for one call. Guards built for the EmptyGuard failover
// (failoverGuardedProvider) never recover either — that call already IS the
// failover.
type contextGuardProvider struct {
	Provider
	window int
	router *Router
	name   string // route provider name, for metering

	// recover enables the quota fallback and timeout retry/handoff.
	recover bool
	// handedOff: the turn already moved to this route (callWithRecovery),
	// so the guard may retry it but never move again.
	handedOff   bool
	mu          sync.Mutex
	switched    *providerRoute // sticky fallback route after a quota error or timeout handoff
	switchedWin int
}

func (r *Router) guardedProvider(rt providerRoute) Provider {
	return &contextGuardProvider{Provider: rt.provider, window: r.contextWindowForRoute(rt), router: r, name: rt.name, recover: true, handedOff: rt.handedOff}
}

// failoverGuardedProvider is guardedProvider without recovery, for a call
// that already IS the failover (EmptyGuard, conduit-1z0g): it must not
// chain to a third route (conduit-31jg.68) nor retry/hand off on a timeout
// (conduit-31jg.79).
func (r *Router) failoverGuardedProvider(rt providerRoute) Provider {
	return &contextGuardProvider{Provider: rt.provider, window: r.contextWindowForRoute(rt), router: r, name: rt.name}
}

// Name reports the provider currently serving the guard's calls — the
// fallback's once a quota error or timeout handoff switched routes, so the
// EmptyGuard asks about the backend that actually failed.
func (g *contextGuardProvider) Name() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.switched != nil {
		return g.switched.name
	}
	return g.Provider.Name()
}

// servingModel reports the model the guard actually sends for a request
// naming reqModel — the sticky fallback's once switched. The EmptyGuard
// uses it for its same-model refusal (conduit-31jg.79): after a handoff to
// z-ai/glm-5.3, a "failover" to z-ai/glm-5.3 is the same route again.
func (g *contextGuardProvider) servingModel(reqModel string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.switched != nil {
		return g.switched.model
	}
	return reqModel
}

func (g *contextGuardProvider) call(ctx context.Context, name string, p Provider, window int, req *GenerateRequest) (*GenerateResponse, error) {
	obs := &callObs{}                                                                    // conduit-2lzv: queue_wait_ms
	release, werr := g.router.acquireProviderSlotObserved(ctx, name, reqModel(req), obs) // conduit-38cz
	if werr != nil {
		return nil, werr
	}
	defer release()
	start := time.Now()
	resp, err := p.GenerateResponse(ctx, fitRequestToWindow(req, window))
	if g.router != nil {
		g.router.meterCall(ctx, name, reqModel(req), resp, err, time.Since(start).Milliseconds(), obs)
	}
	return resp, err
}

// onRoute returns a copy of req carrying rt's model.
func onRoute(req *GenerateRequest, rt *providerRoute) *GenerateRequest {
	out := *req
	out.Model = rt.model
	return &out
}

// guardTarget is one route the guard calls, with its context window.
type guardTarget struct {
	rt  providerRoute
	win int
}

func (g *contextGuardProvider) callTarget(ctx context.Context, t guardTarget, req *GenerateRequest) (*GenerateResponse, error) {
	return g.call(ctx, t.rt.name, t.rt.provider, t.win, req)
}

func (g *contextGuardProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	ctx = beginGuardCall(ctx) // conduit-2lzv: call-log phase + attempt counter
	g.mu.Lock()
	sw, swWin := g.switched, g.switchedWin
	g.mu.Unlock()

	cur := guardTarget{rt: providerRoute{name: g.name, provider: g.Provider}, win: g.window}
	creq := req
	if req != nil {
		cur.rt.model = req.Model
	}
	// Same gate as the tool path in callWithRecovery: move to the fallback
	// only when the request names a model (quotaFallbackNeedsModel), and at
	// most once per turn.
	canMove := !g.handedOff && req != nil && req.Model != ""
	if sw != nil {
		cur = guardTarget{rt: *sw, win: swWin}
		creq = onRoute(req, sw)
		canMove = false
	}

	resp, err := g.callTarget(ctx, cur, creq)
	if err == nil || !g.recover || g.router == nil || req == nil || ctx.Err() != nil {
		return resp, err
	}
	if isEmptyGuardCall(ctx) {
		// conduit-31jg.79: the EmptyGuard owns this call's recovery.
		return resp, err
	}
	switch {
	case IsQuotaError(err):
		if !canMove {
			return resp, err
		}
		fb := g.router.distinctFallbackRoute(cur.rt)
		if fb == nil {
			return resp, err
		}
		log.Printf("[Router] (tool loop) quota error on %q model=%q, switching to fallback model %q on provider %q for the rest of the turn (conduit-31jg.68)",
			cur.rt.name, cur.rt.model, fb.model, fb.name)
		fbResp, fbErr := g.switchTo(ctx, *fb, req)
		if fbErr != nil {
			log.Printf("[Router] (tool loop) quota fallback failed: %v (conduit-31jg.68)", fbErr)
		}
		return fbResp, fbErr
	case IsTransientTimeoutError(err):
		return g.recoverTimeout(ctx, cur, creq, req, canMove, resp, err)
	}
	return resp, err
}

// switchTo calls fb and, on success, makes it the sticky route for the rest
// of the turn.
func (g *contextGuardProvider) switchTo(ctx context.Context, fb providerRoute, req *GenerateRequest) (*GenerateResponse, error) {
	t := guardTarget{rt: fb, win: g.router.contextWindowForRoute(fb)}
	resp, err := g.callTarget(ctx, t, onRoute(req, &fb))
	if err == nil {
		g.mu.Lock()
		g.switched, g.switchedWin = &t.rt, t.win
		g.mu.Unlock()
	}
	return resp, err
}

// recoverTimeout is callWithRecovery's bd-13p/conduit-1w48 timeout step for
// a guarded call (conduit-31jg.79): one same-route retry within the deadline
// budget, then — when canMove and the route has a distinct fallback — ONE
// handoff, sticky on success. creq is req on the current route.
func (g *contextGuardProvider) recoverTimeout(ctx context.Context, cur guardTarget, creq, req *GenerateRequest, canMove bool, resp *GenerateResponse, err error) (*GenerateResponse, error) {
	var fb *providerRoute
	if canMove {
		fb = g.router.distinctFallbackRoute(cur.rt)
	}
	reserve := time.Duration(0)
	if _, hasDeadline := ctx.Deadline(); hasDeadline && fb != nil {
		reserve = recoveryFailoverReserve
	}
	retryCtx, cancelRetry, retryOK := retryBudget(ctx, reserve)
	if retryOK {
		log.Printf("[Router] (tool loop) transient timeout on provider %q model=%q, retrying once (conduit-31jg.79)", cur.rt.name, cur.rt.model)
		resp, err = g.callTarget(retryCtx, cur, creq)
		if err == nil {
			log.Printf("[Router] (tool loop) timeout retry succeeded (conduit-31jg.79)")
		} else {
			log.Printf("[Router] (tool loop) timeout retry failed: %v (conduit-31jg.79)", err)
		}
	} else {
		left, _ := timeLeft(ctx)
		log.Printf("[Router] (tool loop) transient timeout on provider %q model=%q with %s left — skipping same-route retry (conduit-31jg.79)",
			cur.rt.name, cur.rt.model, left.Round(time.Millisecond))
	}
	cancelRetry()

	if err == nil || !IsTransientTimeoutError(err) || fb == nil {
		return resp, err
	}
	if !hasAttemptBudget(ctx) {
		log.Printf("[Router] (tool loop) no deadline budget left for the timeout fallback to %q (conduit-31jg.79)", fb.name)
		return resp, err
	}
	log.Printf("[Router] (tool loop) timeout retries exhausted on provider %q model=%q, handing off to fallback model %q on provider %q for the rest of the turn (conduit-31jg.79)",
		cur.rt.name, cur.rt.model, fb.model, fb.name)
	fbResp, fbErr := g.switchTo(ctx, *fb, req)
	if fbErr != nil {
		log.Printf("[Router] (tool loop) timeout fallback failed: %v (conduit-31jg.79)", fbErr)
	}
	return fbResp, fbErr
}

// meterCall records ONE provider call (conduit-31jg.64): an error to the
// usage tracker, or its tokens (cache included) plus cost. The call's cost
// is stamped on resp.Usage (CostUSD, Priced/UnpricedCalls) so the turn's
// Usage.Add sum carries the exact per-call total into the session cost.
// model "" means the provider's configured default model. It also emits the
// call's line to the persistent call log (call_log.go, conduit-2lzv).
func (r *Router) meterCall(ctx context.Context, providerName, model string, resp *GenerateResponse, err error, latencyMs int64, obs *callObs) {
	if model == "" {
		r.mu.RLock()
		model = r.providerMeta[providerName].DefaultModel
		r.mu.RUnlock()
	}
	defer r.logCall(ctx, providerName, model, resp, err, latencyMs, obs) // conduit-2lzv: after cost is stamped
	if err != nil {
		if r.usageTracker != nil {
			r.usageTracker.RecordError(providerName, model)
		}
		return
	}
	if resp == nil {
		return
	}
	u := &resp.Usage
	cost, priced := r.PricingResolver().Cost(providerName, model, *u)
	u.CostUSD, u.PricedCalls, u.UnpricedCalls = cost, 0, 0
	if priced {
		u.PricedCalls = 1
	} else {
		u.UnpricedCalls = 1
	}
	if r.usageTracker != nil {
		r.usageTracker.RecordUsage(providerName, model, u.PromptTokens, u.CompletionTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, latencyMs)
	}
}
