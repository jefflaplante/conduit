package config

import (
	"log"
	"sort"
)

// EffectivePricingOverrides returns ai.pricing_overrides merged with the
// deprecated ai.smart_routing.pricing_overrides alias. On a key present in
// both, ai.pricing_overrides wins. The result is a fresh map. conduit-31jg.57
func (a AIConfig) EffectivePricingOverrides() map[string]PricingOverride {
	var legacy map[string]PricingOverride
	if a.SmartRouting != nil {
		legacy = a.SmartRouting.PricingOverrides
	}
	if len(a.PricingOverrides) == 0 && len(legacy) == 0 {
		return nil
	}
	out := make(map[string]PricingOverride, len(a.PricingOverrides)+len(legacy))
	for k, v := range legacy {
		out[k] = v
	}
	for k, v := range a.PricingOverrides {
		out[k] = v
	}
	return out
}

// normalizePricingOverrides folds the deprecated alias into
// AIConfig.PricingOverrides (logging a deprecation warning) so every reader
// sees one map. conduit-31jg.57
func (a *AIConfig) normalizePricingOverrides() {
	if a.SmartRouting == nil || len(a.SmartRouting.PricingOverrides) == 0 {
		return
	}
	keys := make([]string, 0, len(a.SmartRouting.PricingOverrides))
	for k := range a.SmartRouting.PricingOverrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	log.Printf("[Config] ai.smart_routing.pricing_overrides is deprecated — move %d model(s) %v to ai.pricing_overrides (merged; ai.pricing_overrides wins on conflicts) (conduit-31jg.57)", len(keys), keys)
	a.PricingOverrides = a.EffectivePricingOverrides()
	a.SmartRouting.PricingOverrides = nil
}

// PricingOverride allows overriding default pricing for a model (USD per
// million tokens). The cache fields are optional: 0 derives them from the
// input price (reads 0.1x, writes 1.25x / 2x by TTL). conduit-31jg.57
type PricingOverride struct {
	InputPerMToken      float64 `json:"input_per_m_token"`
	OutputPerMToken     float64 `json:"output_per_m_token"`
	CacheReadPerMToken  float64 `json:"cache_read_per_m_token,omitempty"`
	CacheWritePerMToken float64 `json:"cache_write_per_m_token,omitempty"`
}
