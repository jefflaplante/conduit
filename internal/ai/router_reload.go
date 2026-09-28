package ai

import (
	"fmt"
	"log"
	"reflect"
	"sort"

	"conduit/internal/config"
)

// Live provider reload (conduit-rmho).
//
// A config hot-reload swaps provider instances in two phases so that a
// failure leaves the router untouched: PrepareProviderReload builds the new
// instances (it can fail), and ProviderReload.Commit swaps them in (it
// cannot). Between the two the caller persists the config, so disk and
// memory never disagree about an applied change.
//
// Concurrency: a turn resolves its provider instance once, at turn start
// (getProvider), and keeps using it for the whole tool loop, so a turn in
// flight during a reload finishes on its old instance. Only new turns, and
// lookups by name that happen later (quota fallback, empty-guard failover,
// vision side calls), see the new instance. Providers whose configuration
// did not change keep their instance (and any state it holds, such as a
// refreshed OAuth token).
//
// Scope: only existing providers can be changed live. Adding or removing a
// provider, changing its type, or changing a claude-code provider (its MCP
// server and session mapper are wired once at startup) is refused here; the
// gateway classifies such changes as restart-only before it gets this far.

// ProviderReload is a prepared, not yet applied, provider reload.
type ProviderReload struct {
	r         *Router
	cfg       config.AIConfig
	providers map[string]Provider
	meta      map[string]ProviderMeta
	cfgs      map[string]config.ProviderConfig
	changed   []string
	committed bool
}

// PrepareProviderReload builds new instances for every provider whose
// configuration in cfg differs from the one the router runs with. Nothing
// on the router changes until Commit. The provider set and types must be
// unchanged, and claude-code providers must be unchanged.
func (r *Router) PrepareProviderReload(cfg config.AIConfig) (*ProviderReload, error) {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	r.mu.RLock()
	current := make(map[string]config.ProviderConfig, len(r.providerCfgs))
	for k, v := range r.providerCfgs {
		current[k] = v
	}
	r.mu.RUnlock()

	next := make(map[string]config.ProviderConfig, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if _, dup := next[p.Name]; dup {
			return nil, fmt.Errorf("provider %q is configured twice", p.Name)
		}
		next[p.Name] = p
	}
	if len(next) != len(current) {
		return nil, fmt.Errorf("adding or removing providers requires a restart")
	}

	pr := &ProviderReload{
		r:         r,
		cfg:       cfg,
		providers: map[string]Provider{},
		meta:      map[string]ProviderMeta{},
		cfgs:      next,
	}
	for name, nc := range next {
		oc, ok := current[name]
		if !ok {
			return nil, fmt.Errorf("adding or removing providers requires a restart (provider %q)", name)
		}
		if reflect.DeepEqual(oc, nc) {
			continue
		}
		if oc.Type != nc.Type {
			return nil, fmt.Errorf("changing the type of provider %q requires a restart", name)
		}
		if nc.Type == "claude-code" {
			return nil, fmt.Errorf("changing claude-code provider %q requires a restart", name)
		}
		p, err := newProviderInstance(cfg, nc)
		if err != nil {
			return nil, err
		}
		pr.providers[name] = p
		pr.meta[name] = providerMetaFor(nc)
		pr.changed = append(pr.changed, name)
	}
	sort.Strings(pr.changed)
	return pr, nil
}

// Changed returns the names of the providers the reload rebuilds, sorted.
func (p *ProviderReload) Changed() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.changed...)
}

// Commit swaps the prepared provider instances and metadata into the
// router and applies the new concurrency limits. Calling it twice is a
// no-op.
func (p *ProviderReload) Commit() {
	if p == nil || p.committed {
		return
	}
	p.committed = true
	r := p.r
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	r.mu.Lock()
	for name, inst := range p.providers {
		r.providers[name] = inst
		r.providerMeta[name] = p.meta[name]
	}
	r.providerCfgs = p.cfgs
	r.mu.Unlock()

	throttled := r.throttle.SetLimits(p.cfg.Providers)
	if len(p.changed) > 0 || len(throttled) > 0 {
		log.Printf("[Router] Live provider reload: rebuilt=%v throttle_limits_changed=%v (conduit-rmho)", p.changed, throttled)
	}
}
