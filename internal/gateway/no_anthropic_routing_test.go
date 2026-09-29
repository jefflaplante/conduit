package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"
	toolstypes "conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// no-anthropic-routing: ai.vision and ai.providers[].routable at the
// gateway layer — vision provider selection, status surfaces, command and
// sub-agent refusals, live-reload classification.

func routableBool(b bool) *bool { return &b }

// liveShapedAI mirrors the live deployment shape with placeholder values:
// z-ai (openai-compatible) default, anthropic kept in config.
func liveShapedAI(anthropicRoutable bool, vision *config.VisionConfig) config.AIConfig {
	var routable *bool
	if !anthropicRoutable {
		routable = routableBool(false)
	}
	return config.AIConfig{
		DefaultProvider: "z-ai",
		Providers: []config.ProviderConfig{
			{Name: "z-ai", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Model: "glm-5.3-flash"},
			{Name: "anthropic", Type: "anthropic", APIKey: "sk-test", Model: "claude-sonnet-4-6", Routable: routable},
		},
		Vision: vision,
	}
}

// newRoutingRouter builds a router from cfg and swaps in mock providers,
// keeping the config-derived metadata (type, routable).
func newRoutingRouter(t *testing.T, cfg config.AIConfig) (*ai.Router, map[string]*ai.MockProvider) {
	t.Helper()
	r, err := ai.NewRouter(cfg, nil)
	require.NoError(t, err)
	mocks := map[string]*ai.MockProvider{}
	for _, p := range cfg.Providers {
		m := ai.NewMockProvider(p.Name)
		m.AddResponse("seen by "+p.Name, nil)
		r.RegisterProvider(p.Name, m)
		mocks[p.Name] = m
	}
	return r, mocks
}

var testPNG = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00}

func TestVisionAdapter_UsesConfiguredProviderAndModel(t *testing.T) {
	// anthropic routable, so the old heuristic WOULD pick it — ai.vision wins.
	cfg := liveShapedAI(true, &config.VisionConfig{Provider: "z-ai", Model: "glm-5.3-flash"})
	r, mocks := newRoutingRouter(t, cfg)
	a := newVisionAdapter(r, cfg.Vision)

	out, err := a.AnalyzeImage(context.Background(), testPNG, "image/png", "what is it?")
	require.NoError(t, err)
	assert.Equal(t, "seen by z-ai", out)
	assert.Equal(t, 0, mocks["anthropic"].GetCallCount())

	calls := mocks["z-ai"].GetCalls()
	require.Len(t, calls, 1)
	req := calls[0].Request
	assert.Equal(t, "glm-5.3-flash", req.Model, "ai.vision.model must reach GenerateSideCall")
	require.Len(t, req.Messages, 1)
	require.Len(t, req.Messages[0].Attachments, 1)
	att := req.Messages[0].Attachments[0]
	assert.Equal(t, "image", att.Type)
	assert.Equal(t, "image/png", att.MediaType)
	assert.Equal(t, testPNG, att.Data)

	// The adapter copies the config: a later mutation does not leak in.
	cfg.Vision.Provider = "anthropic"
	p, m := a.VisionRoute()
	assert.Equal(t, "z-ai", p)
	assert.Equal(t, "glm-5.3-flash", m)
}

func TestVisionAdapter_ConfiguredModelMeteredOnThatModel(t *testing.T) {
	cfg := liveShapedAI(false, &config.VisionConfig{Provider: "z-ai", Model: "glm-vision-x"})
	r, _ := newRoutingRouter(t, cfg)
	obs := &visionMeterObserver{}
	r.GetUsageTracker().SetObserver(obs)
	_, err := newVisionAdapter(r, cfg.Vision).AnalyzeImage(context.Background(), testPNG, "image/png", "p")
	require.NoError(t, err)
	assert.Equal(t, 1, obs.count("z-ai|glm-vision-x"), "pricing/call-log must attribute the configured model")
}

func TestVisionAdapter_UnsetUsesHeuristic(t *testing.T) {
	// Routable anthropic, z-ai (text-only default): heuristic picks anthropic.
	cfg := liveShapedAI(true, nil)
	r, mocks := newRoutingRouter(t, cfg)
	a := newVisionAdapter(r, nil)
	p, m := a.VisionRoute()
	assert.Equal(t, "anthropic", p)
	assert.Equal(t, "", m)
	_, err := a.AnalyzeImage(context.Background(), testPNG, "image/png", "p")
	require.NoError(t, err)
	assert.Equal(t, 1, mocks["anthropic"].GetCallCount())
	assert.Equal(t, "", mocks["anthropic"].GetCalls()[0].Request.Model, "heuristic: provider default model")
}

func TestVisionAdapter_HeuristicSkipsNonRoutable(t *testing.T) {
	cfg := liveShapedAI(false, nil)
	r, mocks := newRoutingRouter(t, cfg)
	a := newVisionAdapter(r, nil)
	p, _ := a.VisionRoute()
	assert.Equal(t, "z-ai", p, "non-routable anthropic skipped → default")
	_, err := a.AnalyzeImage(context.Background(), testPNG, "image/png", "p")
	require.NoError(t, err)
	assert.Equal(t, 0, mocks["anthropic"].GetCallCount())
	assert.Equal(t, 1, mocks["z-ai"].GetCallCount())

	// A routable anthropic-type provider is still preferred over the default.
	cfg.Providers = append(cfg.Providers, config.ProviderConfig{Name: "claude-alt", Type: "anthropic", APIKey: "sk-test2", Model: "claude-haiku-4-5"})
	r2, _ := newRoutingRouter(t, cfg)
	p, _ = newVisionAdapter(r2, nil).VisionRoute()
	assert.Equal(t, "claude-alt", p)
}

func TestVisionAdapter_NonRoutableProviderRefused(t *testing.T) {
	// Validation forbids this config; the router refuses it regardless.
	cfg := liveShapedAI(false, &config.VisionConfig{Provider: "anthropic"})
	r, mocks := newRoutingRouter(t, cfg)
	_, err := newVisionAdapter(r, cfg.Vision).AnalyzeImage(context.Background(), testPNG, "image/png", "p")
	require.Error(t, err)
	var nr *ai.NonRoutableProviderError
	assert.True(t, errors.As(err, &nr), "got %v", err)
	assert.Equal(t, 0, mocks["anthropic"].GetCallCount())
}

// TestVisionAdapter_EndToEndOpenAICompatible drives the real OpenAI provider
// against an httptest server standing in for z.ai: the configured model and
// an image_url data URI must be on the wire.
func TestVisionAdapter_EndToEndOpenAICompatible(t *testing.T) {
	var mu sync.Mutex
	var body map[string]interface{}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		path = r.URL.Path
		_ = json.Unmarshal(b, &body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"a tiny png"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`))
	}))
	defer srv.Close()

	cfg := config.AIConfig{
		DefaultProvider: "z-ai",
		Providers: []config.ProviderConfig{
			{Name: "z-ai", Type: "openai", APIKey: "test-key", BaseURL: srv.URL + "/api/coding/paas/v4", Model: "glm-5.3"},
			{Name: "anthropic", Type: "anthropic", APIKey: "sk-test", Model: "claude-sonnet-4-6", Routable: routableBool(false)},
		},
		Vision: &config.VisionConfig{Provider: "z-ai", Model: "glm-5.3-flash"},
	}
	r, err := ai.NewRouter(cfg, nil)
	require.NoError(t, err)

	out, err := newVisionAdapter(r, cfg.Vision).AnalyzeImage(context.Background(), testPNG, "image/png", "Describe it.")
	require.NoError(t, err)
	assert.Equal(t, "a tiny png", out)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "/api/coding/paas/v4/chat/completions", path)
	assert.Equal(t, "glm-5.3-flash", body["model"], "configured vision model on the wire (not the provider default glm-5.3)")
	msgs, _ := body["messages"].([]interface{})
	require.Len(t, msgs, 1)
	content, _ := msgs[0].(map[string]interface{})["content"].([]interface{})
	require.Len(t, content, 2)
	img := content[0].(map[string]interface{})
	assert.Equal(t, "image_url", img["type"])
	url, _ := img["image_url"].(map[string]interface{})["url"].(string)
	assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(testPNG), url)
	txt := content[1].(map[string]interface{})
	assert.Equal(t, "text", txt["type"])
	assert.Equal(t, "Describe it.", txt["text"])
}

func TestGatewayStatus_ShowsRoutableAndVision(t *testing.T) {
	cfg := liveShapedAI(false, &config.VisionConfig{Provider: "z-ai", Model: "glm-5.3-flash"})
	r, _ := newRoutingRouter(t, cfg)
	g := &Gateway{ai: r, config: &config.Config{AI: cfg}}

	st, err := g.GetGatewayStatus()
	require.NoError(t, err)
	providers, ok := st["providers"].([]map[string]interface{})
	require.True(t, ok, "providers missing: %v", st)
	require.Len(t, providers, 2)
	assert.Equal(t, "anthropic", providers[0]["name"])
	assert.Equal(t, false, providers[0]["routable"])
	assert.Equal(t, false, providers[0]["default"])
	assert.Equal(t, "z-ai", providers[1]["name"])
	assert.Equal(t, true, providers[1]["routable"])
	assert.Equal(t, true, providers[1]["default"])
	assert.Equal(t, []string{"anthropic"}, st["non_routable_providers"])
	assert.Equal(t, map[string]interface{}{"provider": "z-ai", "model": "glm-5.3-flash", "source": "ai.vision"}, st["vision"])

	// Heuristic source when ai.vision is unset.
	g.config.AI.Vision = nil
	st, _ = g.GetGatewayStatus()
	assert.Equal(t, map[string]interface{}{"provider": "z-ai", "model": "", "source": "heuristic"}, st["vision"])
}

func TestRoutingHelpers_CommandsAndListings(t *testing.T) {
	r, _ := newRoutingRouter(t, liveShapedAI(false, nil))
	var anthMeta, zaiMeta ai.ProviderMeta
	for _, m := range r.ListProviders() {
		switch m.Name {
		case "anthropic":
			anthMeta = m
		case "z-ai":
			zaiMeta = m
		}
	}
	assert.Contains(t, routableSuffix(anthMeta), "routable=false")
	assert.Equal(t, "", routableSuffix(zaiMeta))

	// /model switches that would pin a session to anthropic are refused.
	for _, m := range []string{"claude-sonnet-4-6", "anthropic/claude-opus-4-6", "claude-haiku-4-5-20251001"} {
		err := checkModelRoutable(r, m)
		var nr *ai.NonRoutableProviderError
		assert.True(t, errors.As(err, &nr), "%s: %v", m, err)
	}
	assert.NoError(t, checkModelRoutable(r, "glm-5.3"))
	assert.NoError(t, checkModelRoutable(r, "z-ai/glm-5.3"))
	assert.NoError(t, checkModelRoutable(r, ""))
	// /provider anthropic is refused.
	assert.Error(t, r.CheckRoutable("anthropic", ""))

	assert.Equal(t, "anthropic, routable=false: refused", aliasProviderLabel(r, "claude-sonnet-4-6"))
	assert.Equal(t, "z-ai", aliasProviderLabel(r, "glm-5.3"))
}

func TestSpawnSubAgent_RefusesNonRoutableModel(t *testing.T) {
	cfg := liveShapedAI(false, nil)
	r, mocks := newRoutingRouter(t, cfg)
	g := &Gateway{ai: r, config: &config.Config{AI: cfg}} // no aliases → built-in claude-* aliases

	for _, model := range []string{"sonnet", "claude-opus-4-6", "anthropic/claude-sonnet-4-6"} {
		_, err := g.SpawnSubAgentWithCallback(context.Background(), "task", "", model, "", 30, "", "", false, nil)
		require.Error(t, err, model)
		assert.Contains(t, err.Error(), "cannot spawn sub-agent")
		assert.Contains(t, err.Error(), `provider "anthropic" is configured with routable=false`)
	}
	assert.Equal(t, 0, mocks["anthropic"].GetCallCount())
}

// --- live config reload classification ---

const routableReloadJSON = `{
  "port": 18789,
  "ai": {
    "default_provider": "z-ai",
    "providers": [
      {"name": "z-ai", "type": "openai", "api_key": "test-key", "base_url": "BASEURL", "model": "glm-5.3-flash"},
      {"name": "anthropic", "type": "anthropic", "api_key": "sk-test", "model": "claude-sonnet-4-6"},
      {"name": "cc", "type": "claude-code", "model": "sonnet"}
    ]
  },
  "tools": {"enabled_tools": ["Read"], "max_tool_chains": 25}
}
`

func TestApplyConfigUpdate_RoutableIsLive(t *testing.T) {
	f := newReloadFixtureFrom(t, routableReloadJSON)
	before, _ := f.router.GetProvider("anthropic")
	ccBefore, _ := f.router.GetProvider("cc")

	res, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
		"ai.providers.anthropic.routable": false,
		"ai.providers.cc.routable":        false,  // claude-code: routable is live too
		"ai.providers.cc.model":           "opus", // …while this stays restart-only
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	modes := changeModes(res)
	assert.Equal(t, toolstypes.ConfigChangeLive, modes["ai.providers.anthropic.routable"])
	assert.Equal(t, toolstypes.ConfigChangeLive, modes["ai.providers.cc.routable"])
	assert.Equal(t, toolstypes.ConfigChangeRestart, modes["ai.providers.cc.model"])

	assert.False(t, f.router.IsRoutable("anthropic"))
	assert.False(t, f.router.IsRoutable("cc"))
	after, _ := f.router.GetProvider("anthropic")
	assert.True(t, after == before, "routable-only change must not rebuild the provider")
	ccAfter, _ := f.router.GetProvider("cc")
	assert.True(t, ccAfter == ccBefore, "claude-code provider must not be rebuilt")
	cur := findProvider(f.gw.currentConfig().AI.Providers, "cc")
	require.NotNil(t, cur)
	assert.False(t, cur.IsRoutable())
	assert.Equal(t, "sonnet", cur.Model, "restart-only model change must not be applied live")
	assert.Contains(t, f.file(t), `"routable": false`)

	_, err = f.router.GenerateSideCall(context.Background(), "anthropic", &ai.GenerateRequest{})
	var nr *ai.NonRoutableProviderError
	assert.True(t, errors.As(err, &nr), "got %v", err)

	// And back.
	res, err = f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"ai.providers.anthropic.routable": true})
	require.NoError(t, err)
	assert.Equal(t, toolstypes.ConfigChangeLive, changeModes(res)["ai.providers.anthropic.routable"])
	assert.True(t, f.router.IsRoutable("anthropic"))
}

func TestApplyConfigUpdate_RoutableFalseOnDefaultRejected(t *testing.T) {
	f := newReloadFixtureFrom(t, routableReloadJSON)
	_, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"ai.providers.z-ai.routable": false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ai.default_provider "z-ai" is configured with routable=false`)
	assert.True(t, f.router.IsRoutable("z-ai"))
	assert.Equal(t, f.orig, f.file(t), "rejected update must not touch the file")
}

func TestApplyConfigUpdate_VisionIsRestartRequired(t *testing.T) {
	f := newReloadFixtureFrom(t, routableReloadJSON)
	res, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
		"ai.vision": map[string]interface{}{"provider": "z-ai", "model": "glm-5.3-flash"},
	})
	require.NoError(t, err)
	for path, mode := range changeModes(res) {
		assert.True(t, strings.HasPrefix(path, "ai.vision"), path)
		assert.Equal(t, toolstypes.ConfigChangeRestart, mode, path)
	}
	assert.Nil(t, f.gw.currentConfig().AI.Vision, "restart-required: not applied to the running config")
	cfg, err := config.Load(f.path)
	require.NoError(t, err)
	require.NotNil(t, cfg.AI.Vision)
	assert.Equal(t, "glm-5.3-flash", cfg.AI.Vision.Model)

	// Invalid vision providers are rejected before anything changes.
	for _, bad := range []map[string]interface{}{
		{"ai.vision": map[string]interface{}{"provider": "cc"}},      // claude-code
		{"ai.vision": map[string]interface{}{"provider": "missing"}}, // unknown
	} {
		disk := f.file(t)
		_, err := f.gw.ApplyConfigUpdate(context.Background(), bad)
		assert.Error(t, err, "%v", bad)
		assert.Equal(t, disk, f.file(t))
	}
	// Making the vision provider non-routable is rejected.
	_, err = f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"ai.vision.provider": "anthropic"})
	require.NoError(t, err)
	_, err = f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"ai.providers.anthropic.routable": false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ai.vision.provider "anthropic" is configured with routable=false`)
}
