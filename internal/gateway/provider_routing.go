package gateway

import (
	"sort"

	"conduit/internal/ai"
)

// Gateway-side surfaces of ai.providers[].routable (no-anthropic-routing).
// The router enforces routability at its choke points (internal/ai
// router_routable.go); these helpers make it visible in provider listings
// and status, and refuse /provider and /model switches up front so a session
// is never pinned to a provider every turn would then refuse.

// routableSuffix marks a routable=false provider in a provider listing.
func routableSuffix(m ai.ProviderMeta) string {
	if m.Routable() {
		return ""
	}
	return " [routable=false: configured, never routed]"
}

// checkModelRoutable returns the router's NonRoutableProviderError when
// model resolves to a routable=false provider, else nil.
func checkModelRoutable(router *ai.Router, model string) error {
	if router == nil || model == "" {
		return nil
	}
	if p := router.ResolveProviderForModel(model); p != "" {
		return router.CheckRoutable(p, model)
	}
	return nil
}

// aliasProviderLabel names the provider an alias target resolves to, for
// /model listings, flagging a routable=false one.
func aliasProviderLabel(router *ai.Router, model string) string {
	name := router.ResolveProviderForModel(model)
	if name == "" {
		name = router.DefaultProviderName()
	}
	if !router.IsRoutable(name) {
		return name + ", routable=false: refused"
	}
	return name
}

// providerStatusList renders the router's providers for status output,
// sorted by name: name, type, model, routable, default.
func providerStatusList(router *ai.Router) []map[string]interface{} {
	if router == nil {
		return nil
	}
	metas := router.ListProviders()
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	def := router.DefaultProviderName()
	out := make([]map[string]interface{}, 0, len(metas))
	for _, m := range metas {
		out = append(out, map[string]interface{}{
			"name":     m.Name,
			"type":     m.Type,
			"model":    m.DefaultModel,
			"routable": m.Routable(),
			"default":  m.Name == def,
		})
	}
	return out
}

// nonRoutableProviders returns the names of routable=false providers,
// sorted.
func nonRoutableProviders(router *ai.Router) []string {
	if router == nil {
		return nil
	}
	var names []string
	for _, m := range router.ListProviders() {
		if !m.Routable() {
			names = append(names, m.Name)
		}
	}
	sort.Strings(names)
	return names
}

// visionStatus reports where image analysis runs: provider, model ("" =
// provider default) and source ("ai.vision" or "heuristic").
func (g *Gateway) visionStatus() map[string]interface{} {
	if g.ai == nil || g.config == nil {
		return nil
	}
	a := newVisionAdapter(g.ai, g.config.AI.Vision) // ai.vision is restart-required: startup config
	provider, model := a.VisionRoute()
	source := "heuristic"
	if a.cfg != nil {
		source = "ai.vision"
	}
	return map[string]interface{}{"provider": provider, "model": model, "source": source}
}
