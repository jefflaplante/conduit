package ai

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"conduit/internal/config"
)

// initializeProviders sets up AI providers
func (r *Router) initializeProviders(cfg config.AIConfig) error {
	r.throttle = NewProviderThrottle(cfg.Providers) // conduit-38cz

	// Allow empty provider configs for testing
	// The router will still be valid but GenerateResponse will fail if no providers exist
	if len(cfg.Providers) == 0 {
		log.Printf("[Router] No providers configured - router will be empty (testing mode)")
		return nil
	}

	// Initialize providers
	r.providerCfgs = make(map[string]config.ProviderConfig, len(cfg.Providers))
	for _, providerCfg := range cfg.Providers {
		provider, err := newProviderInstance(cfg, providerCfg)
		if err != nil {
			return err
		}
		r.providers[providerCfg.Name] = provider
		r.providerMeta[providerCfg.Name] = providerMetaFor(providerCfg)
		r.providerCfgs[providerCfg.Name] = providerCfg
	}

	return nil
}

// newProviderInstance builds the Provider for one provider config. ai is
// the whole AI section, for ai-level defaults the provider inherits.
func newProviderInstance(ai config.AIConfig, providerCfg config.ProviderConfig) (Provider, error) {
	var provider Provider
	var err error

	// conduit-3dru: provider-level prompt_caching overrides the ai-level
	// block; unset providers inherit it.
	if providerCfg.PromptCaching == nil && ai.PromptCaching != (config.PromptCachingConfig{}) {
		pc := ai.PromptCaching
		providerCfg.PromptCaching = &pc
	}

	switch providerCfg.Type {
	case "anthropic":
		provider, err = NewAnthropicProvider(providerCfg)
	case "openai":
		provider, err = NewOpenAIProvider(providerCfg)
	case "ollama":
		// Ollama is OpenAI-compatible with sensible defaults
		if providerCfg.BaseURL == "" {
			providerCfg.BaseURL = "http://localhost:11434/v1"
		}
		provider, err = NewOpenAIProvider(providerCfg)
	case "claude-code":
		// Session mapper is nil during init; set later via SetSessionMapper.
		provider, err = NewClaudeCodeProvider(providerCfg, nil)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerCfg.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create provider %s: %w", providerCfg.Name, err)
	}
	return provider, nil
}

// providerMetaFor derives the router metadata for a provider config.
func providerMetaFor(providerCfg config.ProviderConfig) ProviderMeta {
	return ProviderMeta{
		Name:          providerCfg.Name,
		Type:          providerCfg.Type,
		DefaultModel:  providerCfg.Model,
		ContextWindow: providerCfg.ContextWindow,
		FallbackModel: providerCfg.FallbackModel, // bd-6tb
		NotRoutable:   !providerCfg.IsRoutable(), // no-anthropic-routing
	}
}

// RegisterProvider adds a provider to the router (useful for testing with mocks)
func (r *Router) RegisterProvider(name string, provider Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = provider
}

// HasProviders returns true if the router has at least one provider configured
func (r *Router) HasProviders() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers) > 0
}

// ListProviders returns metadata for all configured providers.
func (r *Router) ListProviders() []ProviderMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ProviderMeta, 0, len(r.providerMeta))
	for _, meta := range r.providerMeta {
		result = append(result, meta)
	}
	return result
}

// GetProviderMeta returns metadata for a provider by name.
func (r *Router) GetProviderMeta(name string) (ProviderMeta, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	meta, ok := r.providerMeta[name]
	return meta, ok
}

// DefaultProviderName returns the name of the default provider.
func (r *Router) DefaultProviderName() string {
	return r.default_
}

// DefaultModel returns the configured model of the default provider, or ""
// when none is configured (ContextWindowForModel("") then yields
// DefaultContextWindow). conduit-31jg.17: the one place a "which model is
// this turn on" default comes from, replacing hardcoded
// "claude-sonnet-4-20250514" literals in the gateway.
func (r *Router) DefaultModel() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providerMeta[r.default_].DefaultModel
}

// contextWindowForProvider returns the configured context window for a provider,
// or 0 if no override is set (meaning auto-detect from model name).
func (r *Router) contextWindowForProvider(providerName string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if meta, ok := r.providerMeta[providerName]; ok {
		return meta.ContextWindow
	}
	return 0
}

// getProvider returns the named provider under a read lock.
func (r *Router) getProvider(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// GetProvider returns the named provider. This is the exported variant of
// getProvider, used by the gateway to access providers after initialization
// (e.g. to wire a session mapper into the claude-code provider).
func (r *Router) GetProvider(name string) (Provider, bool) {
	return r.getProvider(name)
}

// providerMetaKeys returns the keys of the providerMeta map (for debugging).
// Caller must hold r.mu.
func (r *Router) providerMetaKeys() []string {
	keys := make([]string, 0, len(r.providerMeta))
	for k := range r.providerMeta {
		keys = append(keys, k)
	}
	return keys
}

// ResolveProviderForModel attempts to find the best provider for a given model name.
// Returns the provider name, or "" if no match is found (caller should use default).
//
// no-anthropic-routing: when several providers match a tier, a routable one
// wins. A name that matches ONLY a routable=false provider still resolves
// to it, so the caller's routableProvider check refuses it with a clear
// error instead of silently sending e.g. a claude-* model to the default
// provider.
func (r *Router) ResolveProviderForModel(model string) string {
	if model == "" {
		return ""
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	lower := strings.ToLower(model)

	// Tier 0: explicit provider prefix — "provider/model" format
	// If the model contains a slash, check if the prefix matches a known provider name.
	if idx := strings.Index(lower, "/"); idx > 0 {
		prefix := lower[:idx]
		if _, exists := r.providerMeta[prefix]; exists {
			return prefix
		}
		log.Printf("[Router] ResolveProvider: prefix %q NOT in providerMeta (keys: %v) for model %q", prefix, r.providerMetaKeys(), model)
	}

	// Tier 1: prefix heuristics → provider type (config.InferProviderType)
	if targetType := config.InferProviderType(model); targetType != "" {
		if name := r.pickProviderLocked(func(m ProviderMeta) bool { return m.Type == targetType }); name != "" {
			return name
		}
	}

	// Tier 2: check if model matches any provider's default model
	if name := r.pickProviderLocked(func(m ProviderMeta) bool { return m.DefaultModel == model }); name != "" {
		return name
	}

	// Tier 3: model string is itself a known provider name
	if _, exists := r.providerMeta[lower]; exists {
		return lower
	}

	return ""
}

// pickProviderLocked returns the name of a provider whose meta matches,
// preferring routable providers (then name order, for determinism), or "".
// Caller holds r.mu. no-anthropic-routing
func (r *Router) pickProviderLocked(match func(ProviderMeta) bool) string {
	names := make([]string, 0, len(r.providerMeta))
	for name, meta := range r.providerMeta {
		if match(meta) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if r.providerMeta[name].Routable() {
			return name
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// GenerateResponse generates an AI response for a session
// resolveFallbackRoute returns (fallbackModel, provider to retry on).
// bd-27ud: the fallback model must be sent to ITS OWN provider, not the
// failed one — "z-ai/glm-5.3" retried on Anthropic previously 404'd
// (journal 15:33:21Z Sep 1 2026; 5 sub-agent silent deaths).
// ok=false when the fallback model resolves to no known provider; callers
// must then surface the original error instead of blind-retrying.
func (r *Router) resolveFallbackRoute(failedProvider string) (string, Provider, bool) {
	meta, metaOk := r.GetProviderMeta(failedProvider)
	fallbackModel := "z-ai/glm-5.3" // default fallback (bd-6tb)
	if metaOk && meta.FallbackModel != "" {
		fallbackModel = meta.FallbackModel
	}

	fallbackProviderName := r.ResolveProviderForModel(fallbackModel)
	if fallbackProviderName == "" {
		return "", nil, false
	}
	// no-anthropic-routing: a fallback chain never picks a routable=false
	// provider — the fallback is skipped (as if none resolved), not an error.
	if !r.IsRoutable(fallbackProviderName) {
		log.Printf("[Router] fallback model %q for provider %q resolves to provider %q, which is configured with routable=false — skipping the fallback (no-anthropic-routing)",
			fallbackModel, failedProvider, fallbackProviderName)
		return "", nil, false
	}
	p, exists := r.GetProvider(fallbackProviderName)
	if !exists {
		return "", nil, false
	}
	return fallbackModel, p, true
}

// ResolveEmptyFailover implements ai.EmptyFailoverRouter (conduit-1z0g):
// resolve the provider's configured fallback_model for the empty guard's
// cross-model attempt. Reuses the bd-6tb FallbackModel plumbing.
//
// conduit-15gt: this is a PURE RESOLVER. Same-provider routes are allowed
// here (z-ai → z-ai/glm-5.3 when glm-5.3-flash fails is meaningful failover
// on a different inference path). The guard layer owns the same-model
// refusal — only it knows which model actually failed (req.Model); the
// provider's DefaultModel is NOT the failed model (sessions override it
// per-request), so any router-level same-model check would misfire.
func (r *Router) ResolveEmptyFailover(failedProvider string) (string, Provider, bool) {
	// Unknown failed provider: the quota path defaults to the bd-6tb fallback,
	// but the empty guard only ever asks about a provider that just failed —
	// an unknown name means misconfiguration, so refuse rather than guess.
	if _, metaOk := r.GetProviderMeta(failedProvider); !metaOk {
		log.Printf("[Router] Empty-failover asked about unknown provider %q — refusing (conduit-1z0g)", failedProvider)
		return "", nil, false
	}
	model, p, ok := r.resolveFallbackRoute(failedProvider)
	if !ok {
		return "", nil, false
	}
	// conduit-31jg.18(b): the failover call carries the full tool-loop
	// history too — trim it to the failover route's window.
	return model, r.failoverGuardedProvider(providerRoute{name: p.Name(), provider: p, model: model}), true // conduit-31jg.68: no chained fallback
}
