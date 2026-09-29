package config

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
)

// Routing checks for ai.providers[].routable and ai.vision
// (no-anthropic-routing).
//
// A provider with routable=false stays configured — it is constructed,
// listed, priced and shown in the fuel gauge — but the router never sends a
// call to it. Validation makes the two settings that would REQUIRE routing
// to it fatal (ai.default_provider, ai.vision.provider) and only warns about
// the ones the router refuses (or, for fallback chains, skips) at call time,
// so a config that still carries the old claude-* aliases keeps loading and
// the problem is visible in the startup log.

// InferProviderType maps a bare model name to the provider type it
// conventionally belongs to ("claude-sonnet-4-6" → "anthropic"), or "" when
// the name carries no such hint. It is the single source of the router's
// name-prefix heuristic (ai.Router.ResolveProviderForModel) and of the
// routing warnings below.
func InferProviderType(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "claude-"):
		return "anthropic"
	case strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o1-") || strings.HasPrefix(lower, "o3-"):
		return "openai"
	case strings.HasPrefix(lower, "llama") || strings.HasPrefix(lower, "mistral") ||
		strings.HasPrefix(lower, "mixtral") || strings.HasPrefix(lower, "codellama") ||
		strings.HasPrefix(lower, "deepseek") || strings.HasPrefix(lower, "qwen") ||
		strings.HasPrefix(lower, "phi-") || strings.HasPrefix(lower, "gemma"):
		return "ollama"
	}
	return ""
}

// findProviderConfig returns the provider named name, or nil.
func (a AIConfig) findProviderConfig(name string) *ProviderConfig {
	for i := range a.Providers {
		if a.Providers[i].Name == name {
			return &a.Providers[i]
		}
	}
	return nil
}

// resolveModelProvider mirrors ai.Router.ResolveProviderForModel over the
// config: the provider a model string would be routed to when no provider
// is named, or nil when it would fall back to the default provider. Among
// several candidates a routable one wins, exactly as in the router.
func (a AIConfig) resolveModelProvider(model string) *ProviderConfig {
	if model == "" {
		return nil
	}
	lower := strings.ToLower(model)
	if idx := strings.Index(lower, "/"); idx > 0 {
		if p := a.findProviderConfig(lower[:idx]); p != nil {
			return p
		}
	}
	pick := func(match func(ProviderConfig) bool) *ProviderConfig {
		var fallback *ProviderConfig
		for i := range a.Providers {
			if !match(a.Providers[i]) {
				continue
			}
			if a.Providers[i].IsRoutable() {
				return &a.Providers[i]
			}
			if fallback == nil {
				fallback = &a.Providers[i]
			}
		}
		return fallback
	}
	if t := InferProviderType(model); t != "" {
		if p := pick(func(p ProviderConfig) bool { return p.Type == t }); p != nil {
			return p
		}
	}
	if p := pick(func(p ProviderConfig) bool { return p.Model == model }); p != nil {
		return p
	}
	return a.findProviderConfig(lower)
}

// EffectiveModelAliases returns ai.model_aliases, or the built-in defaults
// when none are configured (what the gateway resolves aliases against).
func (a AIConfig) EffectiveModelAliases() map[string]string {
	if len(a.ModelAliases) > 0 {
		return a.ModelAliases
	}
	return DefaultModelAliases()
}

// validateAIRouting enforces the fatal half of the routing rules:
// ai.default_provider and ai.vision.provider must be routable, and
// ai.vision must name an existing, image-capable provider.
func validateAIRouting(me *multiError, a AIConfig) {
	if p := a.findProviderConfig(a.DefaultProvider); p != nil && !p.IsRoutable() {
		me.add("ai.default_provider %q is configured with routable=false: the default provider must be routable "+
			"(set default_provider to a routable provider, or remove routable=false)", p.Name)
	}
	if a.Vision == nil {
		return
	}
	v := a.Vision
	if strings.TrimSpace(v.Provider) == "" {
		me.add("ai.vision.provider is required when ai.vision is set (name one of ai.providers)")
		return
	}
	p := a.findProviderConfig(v.Provider)
	if p == nil {
		names := make([]string, 0, len(a.Providers))
		for _, pc := range a.Providers {
			names = append(names, pc.Name)
		}
		sort.Strings(names)
		me.add("ai.vision.provider %q is not a configured provider (ai.providers: %s)", v.Provider, strings.Join(names, ", "))
		return
	}
	if !p.IsRoutable() {
		me.add("ai.vision.provider %q is configured with routable=false: vision must use a routable provider", p.Name)
	}
	if p.Type == "claude-code" {
		me.add("ai.vision.provider %q has type claude-code, which sends text only and cannot analyze images; "+
			"use an anthropic, openai or ollama provider", p.Name)
	}
	if idx := strings.Index(v.Model, "/"); idx > 0 {
		prefix := v.Model[:idx]
		if other := a.findProviderConfig(prefix); other != nil && other.Name != p.Name {
			me.add("ai.vision.model %q names provider %q but ai.vision.provider is %q; drop the prefix or make them match",
				v.Model, prefix, p.Name)
		}
	}
}

// RoutingWarnings lists the non-fatal routing problems of the AI section:
// model aliases, the sub-agent default model and provider fallback models
// that resolve to a routable=false provider. The router refuses the first
// two with a clear error and skips the last; the warnings make that visible
// at startup. Sorted for stable output.
func (a AIConfig) RoutingWarnings() []string {
	var out []string
	for alias, model := range a.EffectiveModelAliases() {
		if p := a.resolveModelProvider(model); p != nil && !p.IsRoutable() {
			out = append(out, fmt.Sprintf("ai.model_aliases[%q] = %q resolves to provider %q, which is configured with "+
				"routable=false: requests using this alias will be refused — point the alias at a routable provider's model",
				alias, model, p.Name))
		}
	}
	if m := a.SubagentDefaultModel; m != "" {
		if target, ok := a.EffectiveModelAliases()[strings.ToLower(m)]; ok && target != "" {
			m = target
		}
		if p := a.resolveModelProvider(m); p != nil && !p.IsRoutable() {
			out = append(out, fmt.Sprintf("ai.subagent_default_model %q resolves to provider %q, which is configured with "+
				"routable=false: sub-agents spawned without a model will be refused", a.SubagentDefaultModel, p.Name))
		}
	}
	for _, pc := range a.Providers {
		if pc.FallbackModel == "" {
			continue
		}
		if p := a.resolveModelProvider(pc.FallbackModel); p != nil && !p.IsRoutable() {
			out = append(out, fmt.Sprintf("ai.providers[%q].fallback_model %q resolves to provider %q, which is configured "+
				"with routable=false: the fallback will be skipped", pc.Name, pc.FallbackModel, p.Name))
		}
	}
	sort.Strings(out)
	return out
}

var routingWarnOnce sync.Map // message -> *sync.Once

// warnRouting logs each routing warning once per process (config is parsed
// again by every update_config plan).
func (c *Config) warnRouting() {
	for _, w := range c.AI.RoutingWarnings() {
		o, _ := routingWarnOnce.LoadOrStore(w, &sync.Once{})
		o.(*sync.Once).Do(func() { log.Printf("[Config] WARNING: %s (no-anthropic-routing)", w) })
	}
}
