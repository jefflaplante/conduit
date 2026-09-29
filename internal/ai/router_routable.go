package ai

import (
	"fmt"
	"log"
	"strings"
)

// Non-routable providers (no-anthropic-routing).
//
// ai.providers[].routable=false keeps a provider configured — constructed,
// listed (ListProviders, /provider, gateway status), priced and visible in
// the fuel gauge — while the router never sends it a call. Enforcement lives
// at the router's few provider choke points, so no caller can slip past it:
//
//   - routableProvider: every turn entry (GenerateResponse,
//     GenerateResponseWithTools*, GenerateResponseStreaming, via
//     resolveTurnRoute) and every side call (GenerateSideCall: vision)
//     resolves its provider through it. Whatever named the provider — the
//     default, a session's /provider override, a model alias, a bare model
//     name ("claude-sonnet-4-6" → anthropic), an explicit "anthropic/..."
//     model string, a sub-agent or cron model — a routable=false provider is
//     refused with a NonRoutableProviderError;
//   - resolveFallbackRoute: every fallback chain (quota fallback, timeout
//     handoff, empty-response failover and the context-guard provider's
//     sticky switch, all of which resolve through it) SKIPS a routable=false
//     fallback instead of erroring, leaving the original error in place.
//
// Resolution itself (ResolveProviderForModel) prefers routable providers
// when several match, but still returns a routable=false one when it is the
// only match, so the refusal is explicit rather than a silent reroute of a
// claude-* model to some other backend.

// NonRoutableProviderError is returned when a call would be routed to a
// provider configured with routable=false.
type NonRoutableProviderError struct {
	Provider string
	Model    string // the requested model, when one was named
}

func (e *NonRoutableProviderError) Error() string {
	via := ""
	if e.Model != "" {
		via = fmt.Sprintf(" (requested model %q)", e.Model)
	}
	return fmt.Sprintf("provider %q is configured with routable=false%s: Conduit never routes calls to it — "+
		"use a model on a routable provider, or remove routable=false from ai.providers", e.Provider, via)
}

// IsRoutable reports whether the router may send calls to the named
// provider. Providers without metadata (registered directly, e.g. test
// mocks) are routable.
func (r *Router) IsRoutable(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	meta, ok := r.providerMeta[name]
	return !ok || meta.Routable()
}

// CheckRoutable returns a NonRoutableProviderError when the named provider
// is configured with routable=false, else nil. model is the requested model
// (for the message), "" when none. Unknown providers are not this check's
// concern (nil).
func (r *Router) CheckRoutable(name, model string) error {
	if !r.IsRoutable(name) {
		return &NonRoutableProviderError{Provider: name, Model: model}
	}
	return nil
}

// routableProvider returns the named provider for a call, or an error when
// it is unknown or configured with routable=false. It is the choke point
// every turn and side call resolves its provider through.
func (r *Router) routableProvider(name, model string) (Provider, error) {
	provider, exists := r.getProvider(name)
	if !exists || provider == nil {
		return nil, fmt.Errorf("provider not found: %s", name)
	}
	if err := r.CheckRoutable(name, model); err != nil {
		log.Printf("[Router] refusing call: %v", err)
		return nil, err
	}
	return provider, nil
}

// resolveTurnRoute resolves the (provider, model) a turn runs on from the
// requested provider and model override, and returns the provider through
// the routable check. label tags the journal line ("WithTools",
// "Streaming").
//
// Rules (unchanged from the inline copies it replaces):
//   - a bare provider name used as the model ("ghost") routes to that
//     provider with its default model;
//   - the model decides the provider when none is named, or when the model
//     carries an explicit "provider/" prefix;
//   - otherwise the named provider, else the default provider.
func (r *Router) resolveTurnRoute(label, providerName, modelOverride string) (string, string, Provider, error) {
	requested := modelOverride
	// Handle bare provider name used as model (e.g., model="ghost" where "ghost" is a provider)
	if modelOverride != "" && !strings.Contains(modelOverride, "/") {
		resolved := r.ResolveProviderForModel(modelOverride)
		if resolved != "" && strings.EqualFold(resolved, modelOverride) {
			log.Printf("[Router] Bare provider name %q used as model — routing to provider with its default model", modelOverride)
			providerName = resolved
			modelOverride = ""
		}
	}

	// Resolve provider from model only when:
	// 1. No provider explicitly specified, OR
	// 2. Model has explicit provider prefix (e.g., "ghost/model")
	if modelOverride != "" && (providerName == "" || strings.Contains(modelOverride, "/")) {
		resolved := r.ResolveProviderForModel(modelOverride)
		if resolved != "" {
			if providerName != "" && providerName != resolved {
				log.Printf("[Router] %s: overriding provider %q → %q (from model %q)", label, providerName, resolved, modelOverride)
			}
			providerName = resolved
		}
	}
	if providerName == "" {
		providerName = r.default_
	}

	provider, err := r.routableProvider(providerName, requested)
	if err != nil {
		return "", "", nil, err
	}
	return providerName, modelOverride, provider, nil
}
