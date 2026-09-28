package ai

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"conduit/internal/config"
)

// Per-provider / per-model concurrency throttle (conduit-38cz).
//
// Every provider call goes through one of three choke points —
// callWithRecovery's attempt (first call of a turn), contextGuardProvider.call
// (tool-loop depths, EmptyGuard retries/failover, auto-continue) and
// GenerateSideCall (vision) — and each takes a slot here immediately before
// the HTTP call and releases it when the call returns (success, error, panic;
// streaming calls return at stream end). A slot is held only for the duration
// of one provider call, never across tool execution, so a turn cannot hold a
// slot while waiting on another.
//
// Pools. A call on (provider, model) uses, in order of precedence:
//  1. the provider's model_max_concurrent entry for the model (0 = unlimited);
//  2. a built-in model default for known providers (z.ai: glm-5.3=5,
//     glm-5.3-flash=50 — z.ai enforces these per model);
//  3. the provider-wide max_concurrent, one pool shared by all models;
//  4. otherwise unlimited — still counted, so the fuel gauge shows in-flight.
//
// Deadlines. The wait runs on the attempt's own ctx, so it counts toward the
// turn deadline exactly like a slow response would. The recovery reserves in
// deadline_budget.go are NOT shortened by it: a same-route retry already
// runs on a child ctx that ends recoveryFailoverReserve early, so a retry
// stuck in the queue gives up in time for the handoff, which targets a
// different route (a different pool) and keeps its full reserve. Time spent
// queueing on the first attempt shrinks the remaining budget that
// retryBudget/hasAttemptBudget read, so later stages see it automatically.
// A wait that ends on ctx returns a *ThrottleWaitError wrapping ctx.Err():
// DeadlineExceeded stays a transient timeout for the recovery ladder,
// Canceled stays a cancel. Such an attempt made no provider call, so it is
// not metered.

// knownModelConcurrency is the built-in per-model limit table for providers
// whose limits are documented (conduit-38cz). Keys are lower-case model
// names without a provider prefix.
var knownModelConcurrency = map[string]map[string]int{
	"z.ai": {
		"glm-5.3":       5,
		"glm-5.3-flash": 50,
	},
}

// knownLimitsFamily returns the knownModelConcurrency key for a provider
// config, or "".
func knownLimitsFamily(p config.ProviderConfig) string {
	switch strings.ToLower(p.Name) {
	case "z-ai", "zai", "z.ai":
		return "z.ai"
	}
	if p.BaseURL != "" {
		if u, err := url.Parse(p.BaseURL); err == nil {
			host := strings.ToLower(u.Hostname())
			if host == "z.ai" || strings.HasSuffix(host, ".z.ai") {
				return "z.ai"
			}
		}
	}
	return ""
}

// normalizeThrottleModel lower-cases model and strips a "provider/" prefix.
func normalizeThrottleModel(model string) string {
	return stripProviderPrefix(strings.ToLower(strings.TrimSpace(model)))
}

// Throttle limit sources reported in ProviderSlotStats.Source.
const (
	ThrottleSourceModelConfig    = "model_config"
	ThrottleSourceBuiltin        = "builtin"
	ThrottleSourceProviderConfig = "provider_config"
	ThrottleSourceUnlimited      = "unlimited"
)

// providerLimits is one provider's configured limits.
type providerLimits struct {
	max    int            // provider-wide; 0 = unlimited
	models map[string]int // normalized model → limit (config)
	family string         // knownModelConcurrency key, or ""
}

// throttlePool is one semaphore plus its counters.
type throttlePool struct {
	provider string
	model    string // "" = provider-wide pool
	limit    int    // 0 = unlimited (sem nil)
	source   string
	sem      chan struct{}

	inFlight  atomic.Int64
	waiting   atomic.Int64
	acquired  atomic.Uint64
	queued    atomic.Uint64 // acquisitions that had to wait
	abandoned atomic.Uint64 // waits ended by ctx
	maxWaitNs atomic.Int64
}

type poolKey struct{ provider, model string }

// ProviderThrottle holds the per-provider/model pools. The zero value and
// nil are usable and unlimited.
type ProviderThrottle struct {
	mu     sync.Mutex
	limits map[string]providerLimits
	pools  map[poolKey]*throttlePool
}

// NewProviderThrottle builds a throttle from provider configs.
func NewProviderThrottle(providers []config.ProviderConfig) *ProviderThrottle {
	return &ProviderThrottle{limits: limitsFromConfig(providers), pools: map[poolKey]*throttlePool{}}
}

// limitsFromConfig derives every provider's limits from its config.
func limitsFromConfig(providers []config.ProviderConfig) map[string]providerLimits {
	limits := make(map[string]providerLimits, len(providers))
	for _, p := range providers {
		pl := providerLimits{max: p.MaxConcurrent, family: knownLimitsFamily(p)}
		if len(p.ModelMaxConcurrent) > 0 {
			pl.models = make(map[string]int, len(p.ModelMaxConcurrent))
			for m, n := range p.ModelMaxConcurrent {
				pl.models[normalizeThrottleModel(m)] = n
			}
		}
		limits[p.Name] = pl
	}
	return limits
}

// SetLimits replaces the configured limits on a live config reload
// (conduit-rmho). Pools of providers whose limits changed are dropped and
// rebuilt lazily with the new limits by the next call; pools of unchanged
// providers are kept. A call already holding a slot in a dropped pool keeps
// it and releases it back to that pool, so during the switch a provider
// can briefly run up to (old in-flight + new limit) calls. The dropped
// pools' counters are not carried over. Returns the providers whose limits
// changed, sorted.
func (t *ProviderThrottle) SetLimits(providers []config.ProviderConfig) []string {
	if t == nil {
		return nil
	}
	next := limitsFromConfig(providers)
	t.mu.Lock()
	defer t.mu.Unlock()
	changed := map[string]bool{}
	for name, pl := range next {
		if old, ok := t.limits[name]; !ok || !reflect.DeepEqual(old, pl) {
			changed[name] = true
		}
	}
	for name := range t.limits {
		if _, ok := next[name]; !ok {
			changed[name] = true
		}
	}
	t.limits = next
	for key := range t.pools {
		if changed[key.provider] {
			delete(t.pools, key)
		}
	}
	out := make([]string, 0, len(changed))
	for name := range changed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// resolve returns the pool for (provider, model), creating it on first use.
func (t *ProviderThrottle) pool(provider, model string) *throttlePool {
	nm := normalizeThrottleModel(model)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pools == nil {
		t.pools = map[poolKey]*throttlePool{}
	}
	pl := t.limits[provider]

	key, limit, source := poolKey{provider, nm}, 0, ThrottleSourceUnlimited
	if n, ok := pl.models[nm]; ok {
		limit, source = n, ThrottleSourceModelConfig
	} else if n, ok := knownModelConcurrency[pl.family][nm]; ok {
		limit, source = n, ThrottleSourceBuiltin
	} else if pl.max > 0 {
		key, limit, source = poolKey{provider, ""}, pl.max, ThrottleSourceProviderConfig
	}
	if limit < 0 {
		limit = 0
	}
	if limit == 0 && source != ThrottleSourceUnlimited && source != ThrottleSourceModelConfig {
		source = ThrottleSourceUnlimited
	}
	p := t.pools[key]
	if p == nil {
		p = &throttlePool{provider: provider, model: key.model, limit: limit, source: source}
		if limit > 0 {
			p.sem = make(chan struct{}, limit)
		}
		t.pools[key] = p
	}
	return p
}

// ThrottleWaitError is returned when ctx ended while a call waited for a
// provider slot. It unwraps to ctx.Err().
type ThrottleWaitError struct {
	Provider string
	Model    string
	Limit    int
	Waited   time.Duration
	Err      error
}

func (e *ThrottleWaitError) Error() string {
	m := e.Model
	if m == "" {
		m = "*"
	}
	return fmt.Sprintf("provider %q model %q: gave up waiting %s for a concurrency slot (limit %d): %v",
		e.Provider, m, e.Waited.Round(time.Millisecond), e.Limit, e.Err)
}

func (e *ThrottleWaitError) Unwrap() error { return e.Err }

// Acquire takes a slot for one provider call on (provider, model), waiting
// until one frees or ctx is done. The returned release is idempotent and
// must be called when the call returns (defer it). A nil throttle is
// unlimited.
func (t *ProviderThrottle) Acquire(ctx context.Context, provider, model string) (func(), error) {
	if t == nil {
		return func() {}, nil
	}
	p := t.pool(provider, model)
	if p.sem != nil {
		select {
		case p.sem <- struct{}{}:
		default:
			if err := p.wait(ctx); err != nil {
				return nil, err
			}
		}
	}
	p.inFlight.Add(1)
	p.acquired.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			p.inFlight.Add(-1)
			if p.sem != nil {
				<-p.sem
			}
		})
	}, nil
}

// wait blocks for a slot of a full pool.
func (p *throttlePool) wait(ctx context.Context) error {
	start := time.Now()
	n := p.waiting.Add(1)
	p.queued.Add(1)
	log.Printf("[Throttle] provider %q model %q at limit %d (%d in flight) — queued (%d waiting) (conduit-38cz)",
		p.provider, p.model, p.limit, p.inFlight.Load(), n)
	defer p.waiting.Add(-1)
	select {
	case p.sem <- struct{}{}:
		w := time.Since(start)
		for {
			cur := p.maxWaitNs.Load()
			if int64(w) <= cur || p.maxWaitNs.CompareAndSwap(cur, int64(w)) {
				break
			}
		}
		if w >= time.Second {
			log.Printf("[Throttle] provider %q model %q slot acquired after %s (conduit-38cz)", p.provider, p.model, w.Round(time.Millisecond))
		}
		return nil
	case <-ctx.Done():
		p.abandoned.Add(1)
		err := &ThrottleWaitError{Provider: p.provider, Model: p.model, Limit: p.limit, Waited: time.Since(start), Err: ctx.Err()}
		log.Printf("[Throttle] %v (conduit-38cz)", err)
		return err
	}
}

// ProviderSlotStats is a point-in-time view of one throttle pool.
type ProviderSlotStats struct {
	Provider string `json:"provider"`
	// Model is "" for a provider-wide (max_concurrent) pool.
	Model string `json:"model,omitempty"`
	// Limit is 0 when unlimited.
	Limit    int    `json:"limit"`
	Source   string `json:"source"`
	InFlight int64  `json:"in_flight"`
	Waiting  int64  `json:"waiting"`
	// Acquired counts calls that got a slot; Queued those that had to
	// wait first; Abandoned the waits ended by ctx (cancel/deadline).
	Acquired  uint64 `json:"acquired_total"`
	Queued    uint64 `json:"queued_total"`
	Abandoned uint64 `json:"abandoned_total"`
	MaxWaitMs int64  `json:"max_wait_ms"`
}

// Snapshot returns every pool used so far, sorted by provider then model.
func (t *ProviderThrottle) Snapshot() []ProviderSlotStats {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	pools := make([]*throttlePool, 0, len(t.pools))
	for _, p := range t.pools {
		pools = append(pools, p)
	}
	t.mu.Unlock()
	out := make([]ProviderSlotStats, 0, len(pools))
	for _, p := range pools {
		out = append(out, ProviderSlotStats{
			Provider: p.provider, Model: p.model, Limit: p.limit, Source: p.source,
			InFlight: p.inFlight.Load(), Waiting: p.waiting.Load(),
			Acquired: p.acquired.Load(), Queued: p.queued.Load(), Abandoned: p.abandoned.Load(),
			MaxWaitMs: time.Duration(p.maxWaitNs.Load()).Milliseconds(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// acquireProviderSlot is the router's hook for the call choke points: it
// resolves an empty model to the provider's configured default and takes a
// slot. Safe on a nil router or a router without a throttle.
func (r *Router) acquireProviderSlot(ctx context.Context, providerName, model string) (func(), error) {
	if r == nil || r.throttle == nil {
		return func() {}, nil
	}
	if model == "" {
		r.mu.RLock()
		model = r.providerMeta[providerName].DefaultModel
		r.mu.RUnlock()
	}
	return r.throttle.Acquire(ctx, providerName, model)
}

// acquireProviderSlotObserved is acquireProviderSlot for the call choke
// points that also feed the call log (conduit-2lzv): it records the time
// spent waiting for the slot on obs (queue_wait_ms). Callers take the slot
// BEFORE starting their latency clock, so latency_ms excludes queue wait.
// An abandoned wait returns the error before any provider call; the caller
// returns without metering, so it is neither metered nor logged.
func (r *Router) acquireProviderSlotObserved(ctx context.Context, providerName, model string, obs *callObs) (func(), error) {
	t0 := time.Now()
	release, err := r.acquireProviderSlot(ctx, providerName, model)
	if obs != nil {
		obs.queueWait = time.Since(t0)
	}
	return release, err
}

// ProviderSlots returns the throttle pools' in-flight / waiting counts for
// the fuel gauge and /status.
func (r *Router) ProviderSlots() []ProviderSlotStats {
	if r == nil {
		return nil
	}
	return r.throttle.Snapshot()
}

// reqModel returns req.Model, "" for a nil request.
func reqModel(req *GenerateRequest) string {
	if req == nil {
		return ""
	}
	return req.Model
}
