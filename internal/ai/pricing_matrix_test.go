package ai

import (
	"math"
	"testing"
)

// conduit-31jg.76: resolved prices for every model ID in DefaultPricingMatrix
// plus the real (dated / alias) IDs that reach them by prefix, including the
// prefix-collision cases (opus-4 vs opus-4-5 vs opus-4-6, sonnet-4 vs
// sonnet-4-5 vs sonnet-4-6, gpt-4o vs gpt-4o-mini, gpt-3.5-turbo vs -1106).
// Sources: platform.claude.com/docs/en/about-claude/pricing and
// developers.openai.com/api/docs/pricing, 2026-09-27.

func TestDefaultPricingMatrix_ResolvedPrices(t *testing.T) {
	type price struct{ in, out, cacheRead float64 }
	cases := map[string]price{
		// Anthropic — current
		"claude-fable-5-1":          {10, 50, 0.25},
		"claude-mythos-5-1":         {10, 50, 0.25},
		"claude-fable-5":            {10, 50, 1.0},
		"claude-mythos-5":           {10, 50, 1.0},
		"claude-opus-5-5":           {4, 20, 0.20},
		"claude-opus-5":             {5, 25, 0.50},
		"claude-opus-4-8":           {5, 25, 0.50},
		"claude-opus-4-7":           {5, 25, 0.50},
		"claude-opus-4-6":           {5, 25, 0.50},
		"claude-sonnet-5":           {2, 10, 0.20},
		"claude-sonnet-4-6":         {3, 15, 0.30},
		"claude-haiku-4-5":          {1, 5, 0.10},
		"claude-haiku-4-5-20251001": {1, 5, 0.10},
		// Opus prefix collisions
		"claude-opus-4-5":          {5, 25, 0.50}, // NOT the $15 opus-4 prefix
		"claude-opus-4-5-20251101": {5, 25, 0.50},
		"claude-opus-4-1":          {15, 75, 1.50},
		"claude-opus-4-1-20250805": {15, 75, 1.50},
		"claude-opus-4":            {15, 75, 1.50},
		"claude-opus-4-0":          {15, 75, 1.50},
		"claude-opus-4-20250514":   {15, 75, 1.50},
		"claude-opus-4-6[1m]":      {5, 25, 0.50},
		// Sonnet prefix collisions
		"claude-sonnet-4-5":          {3, 15, 0.30},
		"claude-sonnet-4-5-20250929": {3, 15, 0.30},
		"claude-sonnet-4":            {3, 15, 0.30},
		"claude-sonnet-4-0":          {3, 15, 0.30},
		"claude-sonnet-4-20250514":   {3, 15, 0.30},
		"claude-sonnet-5-20260101":   {2, 10, 0.20}, // not sonnet-4 / not $3
		// Not a real model; kept for the smart-routing cost optimizer.
		"claude-haiku-4": {0.80, 4, 0.08},
		// Anthropic — retired
		"claude-3-5-haiku":           {0.80, 4, 0.08},
		"claude-3-5-haiku-20241022":  {0.80, 4, 0.08},
		"claude-3-5-sonnet":          {3, 15, 0.30},
		"claude-3-5-sonnet-20241022": {3, 15, 0.30},
		"claude-3-opus":              {15, 75, 1.50},
		"claude-3-opus-20240229":     {15, 75, 1.50},
		"claude-3-sonnet":            {3, 15, 0.30},
		"claude-3-haiku":             {0.25, 1.25, 0.025},
		"claude-3-haiku-20240307":    {0.25, 1.25, 0.025},
		// OpenAI
		"gpt-4o":                 {2.50, 10, 1.25},
		"gpt-4o-2024-08-06":      {2.50, 10, 1.25},
		"gpt-4o-2024-05-13":      {5, 15, 0.50},
		"gpt-4o-mini":            {0.15, 0.60, 0.075},
		"gpt-4o-mini-2024-07-18": {0.15, 0.60, 0.075},
		"gpt-4-turbo":            {10, 30, 1.0},
		"gpt-4-turbo-2024-04-09": {10, 30, 1.0},
		"gpt-4":                  {30, 60, 3.0},
		"gpt-4-0613":             {30, 60, 3.0},
		"gpt-3.5-turbo":          {0.50, 1.50, 0.05},
		"gpt-3.5-turbo-0125":     {0.50, 1.50, 0.05},
		"gpt-3.5-turbo-1106":     {1, 2, 0.10},
	}

	// Every matrix key must be covered by the table.
	for id := range DefaultPricingMatrix {
		if _, ok := cases[id]; !ok {
			t.Errorf("matrix entry %q has no resolved-price case", id)
		}
	}

	pr := NewPricingResolver(nil)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	for id, want := range cases {
		p, ok := pr.Resolve("", id)
		if !ok {
			t.Errorf("%s: unpriced, want $%v/$%v", id, want.in, want.out)
			continue
		}
		if !near(p.InputPerMToken, want.in) || !near(p.OutputPerMToken, want.out) {
			t.Errorf("%s: $%v/$%v, want $%v/$%v", id, p.InputPerMToken, p.OutputPerMToken, want.in, want.out)
		}
		// Effective cache-read price: 1M cache-read tokens.
		if got := p.Cost(Usage{CacheReadInputTokens: 1_000_000}, CacheWriteMultiplier5m); !near(got, want.cacheRead) {
			t.Errorf("%s: cache read $%v/MTok, want $%v", id, got, want.cacheRead)
		}
	}
}

// Anthropic cache writes are 1.25x (5m) / 2x (1h) input; OpenAI has no
// cache-write premium.
func TestDefaultPricingMatrix_CacheWrite(t *testing.T) {
	pr := NewPricingResolver(nil)
	for _, tc := range []struct {
		id        string
		mult      float64
		wantWrite float64
	}{
		{"claude-opus-4-5", CacheWriteMultiplier5m, 6.25},
		{"claude-opus-4-1", CacheWriteMultiplier1h, 30},
		{"claude-opus-5-5", CacheWriteMultiplier5m, 5},
		{"claude-sonnet-4-5", CacheWriteMultiplier1h, 6},
		{"claude-3-5-haiku", CacheWriteMultiplier5m, 1},
		{"gpt-4o", CacheWriteMultiplier5m, 2.50},
		{"gpt-4o-mini", CacheWriteMultiplier1h, 0.15},
	} {
		p, _ := pr.Resolve("", tc.id)
		if got := p.Cost(Usage{CacheCreationInputTokens: 1_000_000}, tc.mult); math.Abs(got-tc.wantWrite) > 1e-9 {
			t.Errorf("%s: cache write $%v/MTok, want $%v", tc.id, got, tc.wantWrite)
		}
	}
}

// qwen3.8-flash has no built-in price: it is reported unpriced (the owner
// must add an ai.pricing_overrides entry), never guessed.
func TestDefaultPricingMatrix_Unpriced(t *testing.T) {
	pr := NewPricingResolver(nil)
	for _, id := range []string{"qwen3.8-flash", "openrouter/qwen/qwen3.8-flash"} {
		if p, ok := pr.Resolve("", id); ok {
			t.Errorf("%s: priced $%v/$%v, want unpriced", id, p.InputPerMToken, p.OutputPerMToken)
		}
	}
}
