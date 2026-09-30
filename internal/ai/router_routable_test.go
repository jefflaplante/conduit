package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/config"
)

// no-anthropic-routing: a routable=false provider stays configured and
// listed, but the router never sends it a call.

func boolp(b bool) *bool { return &b }

// stubClaudeBinary puts a no-op `claude` first on PATH for the test.
// NewClaudeCodeProvider only checks that the binary resolves, so a
// claude-code provider builds on machines without Claude Code installed
// (CI runners) and never reaches a real install where it is.
func stubClaudeBinary(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// newRoutableTestRouter builds a router from a live-shaped config (z-ai
// default, anthropic kept but routable=false unless anthropicRoutable) and
// swaps the real provider instances for mocks, keeping the config-derived
// metadata.
func newRoutableTestRouter(t *testing.T, anthropicRoutable bool, zaiFallback string) (*Router, *MockProvider, *MockProvider) {
	t.Helper()
	var routable *bool
	if !anthropicRoutable {
		routable = boolp(false)
	}
	r, err := NewRouter(config.AIConfig{
		DefaultProvider: "z-ai",
		Providers: []config.ProviderConfig{
			{Name: "z-ai", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Model: "glm-5.3-flash", FallbackModel: zaiFallback},
			{Name: "anthropic", Type: "anthropic", APIKey: "sk-test", Model: "claude-sonnet-4-6", Routable: routable},
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	zai := NewMockProvider("z-ai")
	anth := NewMockProvider("anthropic")
	r.RegisterProvider("z-ai", zai)
	r.RegisterProvider("anthropic", anth)
	return r, zai, anth
}

func assertNonRoutable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a routable=false refusal, got nil")
	}
	var nr *NonRoutableProviderError
	if !errors.As(err, &nr) || nr.Provider != "anthropic" {
		t.Fatalf("expected NonRoutableProviderError for anthropic, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), `provider "anthropic" is configured with routable=false`) {
		t.Errorf("unclear error: %v", err)
	}
}

func TestRouter_NonRoutable_RefusesEveryResolutionPath(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
	}{
		{"alias target (haiku)", "", "claude-haiku-4-5-20251001"},
		{"bare claude-* model name", "", "claude-sonnet-4-6"},
		{"explicit anthropic/ model", "", "anthropic/claude-sonnet-4-6"},
		{"explicit prefix overrides a named routable provider", "z-ai", "anthropic/claude-opus-4-6"},
		{"session /provider pin", "anthropic", ""},
		{"bare provider name as model", "", "anthropic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("tools", func(t *testing.T) {
				r, zai, anth := newRoutableTestRouter(t, false, "")
				_, err := r.GenerateResponseWithTools(context.Background(), newFallbackSession(t), "hi", tc.provider, tc.model)
				assertNonRoutable(t, err)
				if anth.GetCallCount() != 0 || zai.GetCallCount() != 0 {
					t.Errorf("calls: anthropic=%d z-ai=%d, want 0/0 (refused, not rerouted)", anth.GetCallCount(), zai.GetCallCount())
				}
			})
			t.Run("streaming", func(t *testing.T) {
				r, zai, anth := newRoutableTestRouter(t, false, "")
				_, err := r.GenerateResponseStreaming(context.Background(), newFallbackSession(t), "hi", tc.provider, tc.model, func(string, bool) {})
				assertNonRoutable(t, err)
				if anth.GetCallCount() != 0 || zai.GetCallCount() != 0 {
					t.Errorf("calls: anthropic=%d z-ai=%d, want 0/0", anth.GetCallCount(), zai.GetCallCount())
				}
			})
		})
	}
}

func TestRouter_NonRoutable_GenerateResponseAndSideCall(t *testing.T) {
	r, _, anth := newRoutableTestRouter(t, false, "")
	_, err := r.GenerateResponse(context.Background(), newFallbackSession(t), "hi", "anthropic")
	assertNonRoutable(t, err)

	_, err = r.GenerateSideCall(context.Background(), "anthropic", &GenerateRequest{Model: "claude-sonnet-4-6"})
	assertNonRoutable(t, err)
	if anth.GetCallCount() != 0 {
		t.Errorf("anthropic called %d times", anth.GetCallCount())
	}
}

func TestRouter_NonRoutable_RoutableProvidersUnaffected(t *testing.T) {
	r, zai, anth := newRoutableTestRouter(t, false, "")
	zai.AddResponse("from z-ai", nil)
	zai.AddResponse("from z-ai again", nil)
	zai.AddResponse("side", nil)

	// Default route and a z-ai model.
	if resp, err := r.GenerateResponseWithTools(context.Background(), newFallbackSession(t), "hi", "", ""); err != nil || resp.GetContent() != "from z-ai" {
		t.Fatalf("default route: %v, %v", resp, err)
	}
	if resp, err := r.GenerateResponseWithTools(context.Background(), newFallbackSession(t), "hi", "", "z-ai/glm-5.3"); err != nil || resp.GetContent() != "from z-ai again" {
		t.Fatalf("z-ai model: %v, %v", resp, err)
	}
	if _, err := r.GenerateSideCall(context.Background(), "z-ai", &GenerateRequest{}); err != nil {
		t.Fatalf("side call on z-ai: %v", err)
	}
	if anth.GetCallCount() != 0 {
		t.Errorf("anthropic called %d times", anth.GetCallCount())
	}

	// Routable anthropic (routable omitted): claude-* still routes there.
	r2, _, anth2 := newRoutableTestRouter(t, true, "")
	anth2.AddResponse("from anthropic", nil)
	if resp, err := r2.GenerateResponseWithTools(context.Background(), newFallbackSession(t), "hi", "", "claude-sonnet-4-6"); err != nil || resp.GetContent() != "from anthropic" {
		t.Fatalf("routable anthropic: %v, %v", resp, err)
	}
}

func TestRouter_NonRoutable_ListedWithFlag(t *testing.T) {
	r, _, _ := newRoutableTestRouter(t, false, "")
	found := map[string]bool{}
	for _, m := range r.ListProviders() {
		found[m.Name] = m.Routable()
	}
	if routable, ok := found["anthropic"]; !ok || routable {
		t.Errorf("anthropic listed=%v routable=%v, want listed and not routable", ok, routable)
	}
	if !found["z-ai"] {
		t.Error("z-ai must be routable")
	}
	if !r.IsRoutable("z-ai") || r.IsRoutable("anthropic") || !r.IsRoutable("mock-only") {
		t.Error("IsRoutable: want z-ai=true anthropic=false unknown(mock)=true")
	}
}

func TestRouter_NonRoutable_FallbackChainsSkip(t *testing.T) {
	// The production-critical shape: z-ai's fallback_model is a claude-*
	// model. With anthropic routable=false every fallback path skips it.
	r, zai, anth := newRoutableTestRouter(t, false, "claude-sonnet-4-6")

	if _, _, ok := r.resolveFallbackRoute("z-ai"); ok {
		t.Error("resolveFallbackRoute must skip a routable=false fallback")
	}
	if _, _, ok := r.ResolveEmptyFailover("z-ai"); ok {
		t.Error("empty-response failover must skip a routable=false fallback")
	}
	if fb := r.distinctFallbackRoute(providerRoute{name: "z-ai", provider: zai, model: "glm-5.3-flash"}); fb != nil {
		t.Errorf("timeout handoff / guard switch must skip, got %+v", fb)
	}

	// Quota error on z-ai: no fallback attempt, the original error surfaces.
	zai.SetResponses([]MockResponse{{Error: fmt.Errorf("400 - out of extra usage: quota exhausted")}})
	_, err := r.GenerateResponseWithTools(context.Background(), newFallbackSession(t), "hi", "", "glm-5.3-flash")
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("expected the original quota error, got %v", err)
	}
	var nr *NonRoutableProviderError
	if errors.As(err, &nr) {
		t.Errorf("a skipped fallback must not turn into a routable error: %v", err)
	}
	if anth.GetCallCount() != 0 {
		t.Errorf("anthropic called %d times by a fallback chain", anth.GetCallCount())
	}

	// Same config, anthropic routable: the fallback resolves (control).
	r2, _, _ := newRoutableTestRouter(t, true, "claude-sonnet-4-6")
	if model, p, ok := r2.resolveFallbackRoute("z-ai"); !ok || p.Name() != "anthropic" || model != "claude-sonnet-4-6" {
		t.Errorf("routable control: model=%q ok=%v", model, ok)
	}
}

func TestRouter_NonRoutable_RoutableSameTypeProviderPreferred(t *testing.T) {
	r, err := NewRouter(config.AIConfig{
		DefaultProvider: "z-ai",
		Providers: []config.ProviderConfig{
			{Name: "z-ai", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Model: "glm-5.3-flash"},
			// "anthropic" sorts first; the routable one must still win.
			{Name: "anthropic", Type: "anthropic", APIKey: "sk-test", Model: "claude-sonnet-4-6", Routable: boolp(false)},
			{Name: "anthropic-work", Type: "anthropic", APIKey: "sk-test2", Model: "claude-sonnet-4-6"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ { // map order must not matter
		if got := r.ResolveProviderForModel("claude-sonnet-4-6"); got != "anthropic-work" {
			t.Fatalf("ResolveProviderForModel = %q, want anthropic-work", got)
		}
	}
	// Explicit prefix still names the non-routable one (and is refused).
	if got := r.ResolveProviderForModel("anthropic/claude-sonnet-4-6"); got != "anthropic" {
		t.Errorf("explicit prefix resolved to %q", got)
	}
}

func TestProviderReload_RoutableOnlyChangeIsMetadataSwap(t *testing.T) {
	stubClaudeBinary(t)
	cfg := config.AIConfig{
		DefaultProvider: "z-ai",
		Providers: []config.ProviderConfig{
			{Name: "z-ai", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Model: "glm-5.3-flash"},
			{Name: "anthropic", Type: "anthropic", APIKey: "sk-test", Model: "claude-sonnet-4-6"},
			{Name: "cc", Type: "claude-code", Model: "sonnet"},
		},
	}
	r, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := r.GetProvider("anthropic")
	ccBefore, _ := r.GetProvider("cc")

	next := cfg
	next.Providers = append([]config.ProviderConfig(nil), cfg.Providers...)
	next.Providers[1].Routable = boolp(false)
	next.Providers[2].Routable = boolp(false) // claude-code: live too
	prep, err := r.PrepareProviderReload(next)
	if err != nil {
		t.Fatalf("PrepareProviderReload: %v", err)
	}
	if len(prep.Changed()) != 0 {
		t.Errorf("rebuilt %v, want none (routable-only change)", prep.Changed())
	}
	if got := prep.RoutabilityChanged(); len(got) != 2 || got[0] != "anthropic" || got[1] != "cc" {
		t.Errorf("RoutabilityChanged = %v", got)
	}
	if !r.IsRoutable("anthropic") {
		t.Error("prepare must not change the router")
	}
	prep.Commit()
	if r.IsRoutable("anthropic") || r.IsRoutable("cc") {
		t.Error("commit did not apply routable=false")
	}
	if after, _ := r.GetProvider("anthropic"); after != before {
		t.Error("anthropic instance was rebuilt")
	}
	if after, _ := r.GetProvider("cc"); after != ccBefore {
		t.Error("claude-code instance was rebuilt")
	}
	_, err = r.GenerateSideCall(context.Background(), "anthropic", &GenerateRequest{})
	assertNonRoutable(t, err)

	// Back to routable (omitted) — live again.
	prep, err = r.PrepareProviderReload(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prep.Commit()
	if !r.IsRoutable("anthropic") || !r.IsRoutable("cc") {
		t.Error("routable not restored")
	}
}
