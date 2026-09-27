package ai

import (
	"bytes"
	"encoding/json"
	"log"
	"math"
	"strings"
	"testing"

	"conduit/internal/config"
)

// conduit-31jg.57: pricing resolver — config overrides, provider-prefixed
// IDs, cache pricing, unknown models.

// ownerOverridesJSON mirrors the shape (and a subset of the entries) of the
// owner's ai.pricing_overrides block.
const ownerOverridesJSON = `{
  "glm-5.3": {"input_per_m_token": 1.4, "output_per_m_token": 4.4},
  "glm-5.3-flash": {"input_per_m_token": 0.15, "output_per_m_token": 0.5},
  "glm-5": {"input_per_m_token": 0.72, "output_per_m_token": 2.3},
  "glm-5-turbo": {"input_per_m_token": 1.2, "output_per_m_token": 4.0},
  "openrouter/deepseek/deepseek-v4.1-flash": {"input_per_m_token": 0.30, "output_per_m_token": 1.20},
  "openrouter/z-ai/glm-5.3-flash": {"input_per_m_token": 0.15, "output_per_m_token": 0.50}
}`

func approx(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %.9f, want %.9f", what, got, want)
	}
}

func ownerResolver(t *testing.T) *PricingResolver {
	t.Helper()
	var ov map[string]config.PricingOverride
	if err := json.Unmarshal([]byte(ownerOverridesJSON), &ov); err != nil {
		t.Fatal(err)
	}
	return NewPricingResolver(ov)
}

func TestPricingResolver_ProviderPrefixedIDs(t *testing.T) {
	pr := ownerResolver(t)
	cases := []struct {
		provider, model string
		in, out         float64
	}{
		{"z-ai", "glm-5.3", 1.4, 4.4},                                   // bare default model
		{"", "z-ai/glm-5.3", 1.4, 4.4},                                  // provider-prefixed override form
		{"z-ai", "z-ai/glm-5.3-flash", 0.15, 0.5},                       // prefix already present
		{"openrouter", "deepseek/deepseek-v4.1-flash", 0.30, 1.20},      // provider's model, key is provider/model
		{"", "openrouter/deepseek/deepseek-v4.1-flash", 0.30, 1.20},     // full key
		{"openrouter", "z-ai/glm-5.3-flash", 0.15, 0.50},                // nested vendor
		{"z-ai", "GLM-5-Turbo", 1.2, 4.0},                               // case-insensitive, exact beats prefix "glm-5"
		{"z-ai", "glm-5-20260101", 0.72, 2.3},                           // dated suffix → longest prefix
		{"anthropic", "claude-sonnet-4-6", 3.0, 15.0},                   // built-in
		{"claude-code", "claude-code/claude-sonnet-4-6", 3.0, 15.0},     // alias form
		{"openrouter", "anthropic/claude-haiku-4-5-20251001", 1.0, 5.0}, // vendor-prefixed + dated
	}
	for _, c := range cases {
		p, ok := pr.Resolve(c.provider, c.model)
		if !ok {
			t.Errorf("Resolve(%q,%q) unknown", c.provider, c.model)
			continue
		}
		if p.InputPerMToken != c.in || p.OutputPerMToken != c.out {
			t.Errorf("Resolve(%q,%q) = %+v, want in=%v out=%v", c.provider, c.model, p, c.in, c.out)
		}
	}
}

func TestPricingResolver_PrefixRespectsIDBoundary(t *testing.T) {
	pr := NewPricingResolver(nil)
	// Opus 4.6 must not fall into the legacy "claude-opus-4" ($15) entry.
	p, ok := pr.Resolve("anthropic", "claude-opus-4-6")
	if !ok || p.InputPerMToken != 5.0 {
		t.Errorf("claude-opus-4-6 = %+v ok=%v, want $5 input", p, ok)
	}
	// "glm-5.4" must not be priced as "glm-5" ('.' is not a boundary).
	own := ownerResolver(t)
	if _, ok := own.Resolve("z-ai", "glm-5.4"); ok {
		t.Error("glm-5.4 resolved via glm-5 prefix")
	}
	if p, ok := pr.Resolve("", "claude-sonnet-4-6[1m]"); !ok || p.InputPerMToken != 3.0 {
		t.Errorf("claude-sonnet-4-6[1m] = %+v ok=%v", p, ok)
	}
}

func TestPricingResolver_CachePricing(t *testing.T) {
	pr := NewPricingResolver(nil)
	u := Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000,
		CacheCreationInputTokens: 1_000_000, CacheReadInputTokens: 1_000_000}
	cost, ok := pr.Cost("anthropic", "claude-sonnet-4-6", u)
	if !ok {
		t.Fatal("sonnet 4.6 unpriced")
	}
	// $3 input + $15 output + $3.75 write (1.25x) + $0.30 read (0.1x)
	approx(t, "sonnet 5m cost", cost, 3+15+3.75+0.30)

	// 1-hour TTL: writes at 2x.
	pr1h := NewPricingResolverFromConfig(config.AIConfig{PromptCaching: config.PromptCachingConfig{ExtendedTTL: true}})
	cost, _ = pr1h.Cost("anthropic", "claude-sonnet-4-6", Usage{CacheCreationInputTokens: 1_000_000})
	approx(t, "sonnet 1h write", cost, 6.0)

	// Provider-level prompt_caching override wins over the global block.
	prProv := NewPricingResolverFromConfig(config.AIConfig{
		PromptCaching: config.PromptCachingConfig{ExtendedTTL: true},
		Providers:     []config.ProviderConfig{{Name: "anthropic", PromptCaching: &config.PromptCachingConfig{ExtendedTTL: false}}},
	})
	cost, _ = prProv.Cost("anthropic", "claude-sonnet-4-6", Usage{CacheCreationInputTokens: 1_000_000})
	approx(t, "provider 5m write", cost, 3.75)

	// Explicit published cache-read price (Fable 5.1: $0.25, not 0.1x).
	cost, _ = pr.Cost("anthropic", "claude-fable-5-1", Usage{CacheReadInputTokens: 1_000_000})
	approx(t, "fable 5.1 read", cost, 0.25)

	// Override with no cache fields derives them from input.
	own := ownerResolver(t)
	cost, _ = own.Cost("z-ai", "glm-5.3", Usage{CacheReadInputTokens: 1_000_000, CacheCreationInputTokens: 1_000_000})
	approx(t, "glm cache", cost, 1.4*0.1+1.4*1.25)
}

func TestPricingResolver_GLMTurnNonZero(t *testing.T) {
	pr := ownerResolver(t)
	cost, ok := pr.Cost("z-ai", "glm-5.3", Usage{PromptTokens: 20_000, CompletionTokens: 1_000})
	if !ok || cost <= 0 {
		t.Fatalf("glm-5.3 cost = %v ok=%v, want > 0", cost, ok)
	}
	approx(t, "glm-5.3", cost, 20_000*1.4/1e6+1_000*4.4/1e6)
}

func TestPricingResolver_UnknownModelLoggedOnceNotFree(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	pr := NewPricingResolver(nil)
	for i := 0; i < 3; i++ {
		cost, ok := pr.Cost("ghost", "Qwen3.5-35B-A3B-Q4_K_M.gguf", Usage{PromptTokens: 100})
		if ok {
			t.Fatal("unknown model reported as priced")
		}
		if cost != 0 {
			t.Fatalf("unknown model cost = %v", cost)
		}
	}
	if n := strings.Count(buf.String(), "no price for model"); n != 1 {
		t.Errorf("unknown-model warning logged %d times, want 1:\n%s", n, buf.String())
	}
	// OpenRouter :free variants are known and free.
	if cost, ok := pr.Cost("openrouter", "nvidia/nemotron-3.5-lightning:free", Usage{PromptTokens: 100}); !ok || cost != 0 {
		t.Errorf(":free = %v ok=%v, want known $0", cost, ok)
	}
}

func TestUsageTracker_UsesResolverAndFlagsUnpriced(t *testing.T) {
	ut := NewUsageTracker()
	ut.SetPricingResolver(ownerResolver(t))
	ut.RecordUsage("z-ai", "glm-5.3", 1_000_000, 0, 0, 0, 10)
	ut.RecordUsage("anthropic", "claude-sonnet-4-6", 0, 0, 0, 1_000_000, 10)
	ut.RecordUsage("ghost", "mystery-model", 1000, 1000, 0, 0, 10)

	approx(t, "total", ut.TotalCost(), 1.4+0.30)
	mr, _ := ut.GetModelUsage("mystery-model")
	if !mr.Unpriced {
		t.Error("mystery-model not flagged unpriced")
	}
	pr, _ := ut.GetProviderUsage("ghost")
	if pr.UnpricedRequests != 1 {
		t.Errorf("ghost unpriced requests = %d", pr.UnpricedRequests)
	}
	an, _ := ut.GetProviderUsage("anthropic")
	approx(t, "cache savings", an.CacheSavings, 3.0-0.30)
}

func TestRouter_TurnCostResolvesDefaultRoute(t *testing.T) {
	var ov map[string]config.PricingOverride
	_ = json.Unmarshal([]byte(ownerOverridesJSON), &ov)
	cfg := config.AIConfig{DefaultProvider: "z-ai", PricingOverrides: ov}
	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.providerMeta["z-ai"] = ProviderMeta{Name: "z-ai", Type: "openai", DefaultModel: "glm-5.3"}
	r.providerMeta["openrouter"] = ProviderMeta{Name: "openrouter", Type: "openai", DefaultModel: "deepseek/deepseek-v4.1-flash"}

	u := Usage{PromptTokens: 1_000_000}
	if c, ok := r.TurnCost("", "", u); !ok || c != 1.4 {
		t.Errorf("default route cost = %v ok=%v, want 1.4 (glm-5.3)", c, ok)
	}
	if c, ok := r.TurnCost("openrouter", "", u); !ok || math.Abs(c-0.30) > 1e-9 {
		t.Errorf("openrouter default cost = %v ok=%v, want 0.30", c, ok)
	}
}
