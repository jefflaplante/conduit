package ai

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// conduit-31jg.75: metered one-shot ("side") calls.
//
// Some provider calls are made on behalf of a tool rather than by the turn's
// own generation loop — the Image tool's vision analysis is the first. They
// used to call the provider directly, so they never reached meterCall: no
// fuel-gauge/TokenWindowTracker entry and no cost.
//
// GenerateSideCall runs such a call through meterCall (tokens, cache tokens
// and cost priced on the provider + model that served it) and, when ctx
// carries a SideCallLedger, adds the call's usage to it. The turn runner
// attaches a ledger to each turn's ctx; tools run inside that ctx, so a side
// call made by a tool during a turn is folded into that turn's request cost
// and therefore into session_total_cost. A side call outside any turn (no
// ledger) is still metered to the usage tracker; it just has no session to
// be charged to.

// SideCallLedger accumulates the usage of side calls made under one ctx
// (one turn). Safe for concurrent use (parallel tool execution).
type SideCallLedger struct {
	mu    sync.Mutex
	usage Usage
}

func (l *SideCallLedger) add(u Usage) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.usage.Add(u)
	l.mu.Unlock()
}

// Usage returns the summed usage of the side calls recorded so far. Its
// CostUSD / PricedCalls / UnpricedCalls fields are the metered cost; nil
// ledger returns the zero Usage.
func (l *SideCallLedger) Usage() Usage {
	if l == nil {
		return Usage{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.usage
}

type sideCallLedgerKey struct{}

// WithSideCallLedger returns a ctx carrying a fresh ledger. An inner ledger
// (e.g. a sub-agent's nested turn) shadows the outer one, so a side call is
// charged to exactly one turn.
func WithSideCallLedger(ctx context.Context) (context.Context, *SideCallLedger) {
	l := &SideCallLedger{}
	return context.WithValue(ctx, sideCallLedgerKey{}, l), l
}

// SideCallLedgerFrom returns the ledger on ctx, or nil.
func SideCallLedgerFrom(ctx context.Context) *SideCallLedger {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(sideCallLedgerKey{}).(*SideCallLedger)
	return l
}

// GenerateSideCall sends req to the named provider as a single metered call
// (no history, system prompt, tools or recovery ladder). req.Model "" means
// the provider's configured default model, which is also what the call is
// metered and priced as. The returned response carries the call's cost on
// resp.Usage (CostUSD, PricedCalls/UnpricedCalls).
func (r *Router) GenerateSideCall(ctx context.Context, providerName string, req *GenerateRequest) (*GenerateResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("side call: AI router not available")
	}
	provider, ok := r.getProvider(providerName)
	if !ok || provider == nil {
		return nil, fmt.Errorf("side call: provider %q not available", providerName)
	}
	model := ""
	if req != nil {
		model = req.Model
	}
	// no-anthropic-routing: side calls (vision) obey routable=false too.
	if err := r.CheckRoutable(providerName, model); err != nil {
		return nil, fmt.Errorf("side call: %w", err)
	}
	obs := &callObs{} // conduit-2lzv: queue_wait_ms
	// conduit-38cz; the side-call scope gives an abandoned wait's queue_*
	// record the side-call phase (conduit-3j08).
	release, werr := r.acquireProviderSlotObserved(beginSideCall(ctx), providerName, model, obs)
	if werr != nil {
		return nil, werr
	}
	defer release()
	start := time.Now()
	resp, err := provider.GenerateResponse(ctx, req)
	r.meterCall(beginSideCall(ctx), providerName, model, resp, err, time.Since(start).Milliseconds(), obs)
	if err == nil && resp != nil {
		SideCallLedgerFrom(ctx).add(resp.Usage)
	}
	return resp, err
}
