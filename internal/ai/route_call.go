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
	attempt := func(rt providerRoute) (*GenerateResponse, int64, error) {
		areq := *req
		areq.Model = rt.model
		trimRequestToFitContext(&areq, r.contextWindowForRoute(rt))
		start := time.Now()
		var resp *GenerateResponse
		var err error
		if opts.stream != nil {
			if sp, ok := rt.provider.(StreamingProvider); ok {
				resp, err = sp.GenerateResponseStreaming(ctx, &areq, opts.stream.callback())
			} else {
				// Non-streaming fallback route: the final content still
				// reaches the client (the gateway delivers the final text).
				log.Printf("[Router] (%s) provider %q cannot stream — non-streaming attempt, final content will be delivered whole", opts.phase, rt.name)
				resp, err = rt.provider.GenerateResponse(ctx, &areq)
			}
		} else {
			resp, err = rt.provider.GenerateResponse(ctx, &areq)
		}
		if err == nil {
			served = areq
		}
		return resp, time.Since(start).Milliseconds(), err
	}

	resp, latencyMs, err := attempt(cur)

	if err != nil && IsQuotaError(err) && !(opts.quotaFallbackNeedsModel && primary.model == "") {
		if fbModel, fbProvider, ok := r.resolveFallbackRoute(primary.name); ok {
			fb := providerRoute{name: fbProvider.Name(), provider: fbProvider, model: fbModel}
			log.Printf("[Router] (%s) quota error on %q model=%q, retrying with fallback model %q on provider %q (bd-27ud)",
				opts.phase, primary.name, primary.model, fb.model, fb.name)
			cur = fb
			resp, latencyMs, err = attempt(cur)
			if err == nil {
				log.Printf("[Router] (%s) fallback retry succeeded (bd-27ud): %q -> %q", opts.phase, primary.model, fb.model)
			} else {
				log.Printf("[Router] (%s) fallback retry failed: %v (bd-27ud)", opts.phase, err)
			}
		}
	}

	if err != nil && IsTransientTimeoutError(err) && ctx.Err() == nil {
		// conduit-31jg.18(a): retry the PAIR that timed out.
		log.Printf("[Router] (%s) transient timeout on provider %q model=%q, retrying once (bd-13p)", opts.phase, cur.name, cur.model)
		resp, latencyMs, err = attempt(cur)
		if err == nil {
			log.Printf("[Router] (%s) timeout retry succeeded (bd-13p)", opts.phase)
		} else {
			log.Printf("[Router] (%s) timeout retry failed: %v (bd-13p)", opts.phase, err)
		}
	}

	// conduit-31jg.46: providers retry overload/rate-limit errors only while
	// nothing has been streamed (anthropic_retry.go). When text already
	// reached the client, one muted retry on the same route; the final
	// content replaces the partial stream.
	if err != nil && opts.stream != nil && opts.stream.emitted() && IsRetryableOverloadError(err) && ctx.Err() == nil {
		log.Printf("[Router] (%s) overload after %d streamed bytes on provider %q model=%q — one muted retry (conduit-31jg.46)",
			opts.phase, opts.stream.emittedBytes(), cur.name, cur.model)
		resp, latencyMs, err = attempt(cur)
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
type contextGuardProvider struct {
	Provider
	window int
}

func (r *Router) guardedProvider(rt providerRoute) Provider {
	return &contextGuardProvider{Provider: rt.provider, window: r.contextWindowForRoute(rt)}
}

func (g *contextGuardProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	return g.Provider.GenerateResponse(ctx, fitRequestToWindow(req, g.window))
}
