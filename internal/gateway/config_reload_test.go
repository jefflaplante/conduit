package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
	toolstypes "conduit/internal/tools/types"
)

const reloadSecret = "sk-zai-SECRET-value-123"

// reloadConfigJSON is a config file with ${ENV} placeholders, an unknown key
// and a deprecated key, all of which a live update must preserve.
const reloadConfigJSON = `{
  "port": 18789,
  "unknown_future_key": {"keep": "me"},
  "ai": {
    "default_provider": "z-ai",
    "providers": [
      {
        "name": "z-ai",
        "type": "openai",
        "api_key": "${CONDUIT_TEST_ZAI_KEY}",
        "base_url": "BASEURL",
        "model": "glm-5.3",
        "timeout_seconds": 300,
        "max_concurrent": 2
      },
      {
        "name": "cc",
        "type": "claude-code",
        "model": "sonnet"
      }
    ],
    "smart_routing": {"enabled": false}
  },
  "tools": {
    "enabled_tools": ["Read"],
    "max_tool_chains": 25,
    "sandbox": {"allowed_paths": ["/tmp"]}
  }
}
`

type reloadFixture struct {
	gw     *Gateway
	router *ai.Router
	path   string
	orig   string // initial config.json content
	logs   *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newReloadFixture(t *testing.T) *reloadFixture {
	t.Helper()
	t.Setenv("CONDUIT_TEST_ZAI_KEY", reloadSecret)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	orig := strings.Replace(reloadConfigJSON, "BASEURL", srv.URL, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.DataDir = dir
	router, err := ai.NewRouter(cfg.AI, nil)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	logs := &syncBuffer{}
	gw := &Gateway{config: cfg, ai: router, logger: slog.New(slog.NewTextHandler(logs, nil))}
	gw.SetConfigPath(path)
	return &reloadFixture{gw: gw, router: router, path: path, orig: orig, logs: logs}
}

func (f *reloadFixture) file(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertNoSecret(t *testing.T, what string, v interface{}) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), reloadSecret) {
		t.Errorf("%s leaks the secret: %s", what, b)
	}
}

func changeModes(res *toolstypes.ConfigUpdateResult) map[string]string {
	m := map[string]string{}
	for _, c := range res.Changes {
		m[c.Path] = c.Mode
	}
	return m
}

func TestApplyConfigUpdate_ProviderChangeAppliesLive(t *testing.T) {
	f := newReloadFixture(t)
	oldProvider, _ := f.router.GetProvider("z-ai")
	ccProvider, _ := f.router.GetProvider("cc")

	res, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
		"ai.providers.z-ai.max_concurrent": 5.0,
		"ai":                               map[string]interface{}{"providers": map[string]interface{}{"z-ai": map[string]interface{}{"timeout_seconds": 600.0}}},
	})
	if err != nil {
		t.Fatalf("ApplyConfigUpdate: %v", err)
	}
	if !res.Applied {
		t.Fatal("not applied")
	}
	modes := changeModes(res)
	for _, k := range []string{"ai.providers.z-ai.max_concurrent", "ai.providers.z-ai.timeout_seconds"} {
		if modes[k] != toolstypes.ConfigChangeLive {
			t.Errorf("%s mode = %q, want live (%v)", k, modes[k], modes)
		}
	}
	if res.Readback["ai.providers.z-ai.timeout_seconds"] != 600.0 {
		t.Errorf("readback = %v", res.Readback)
	}

	// Router: new instance for z-ai, cc untouched, throttle limit 5.
	if p, _ := f.router.GetProvider("z-ai"); p == oldProvider {
		t.Error("z-ai provider not rebuilt")
	}
	if p, _ := f.router.GetProvider("cc"); p != ccProvider {
		t.Error("claude-code provider was rebuilt")
	}
	callProvider(t, f.router, "z-ai")
	limit := -1
	for _, s := range f.router.ProviderSlots() {
		if s.Provider == "z-ai" {
			limit = s.Limit
		}
	}
	if limit != 5 {
		t.Errorf("throttle limit = %d, want 5", limit)
	}

	// Effective config and readback reflect it; secrets stay redacted.
	cur := f.gw.currentConfig()
	if p := findProvider(cur.AI.Providers, "z-ai"); p == nil || p.TimeoutSeconds != 600 || p.MaxConcurrent != 5 {
		t.Errorf("current config provider = %+v", p)
	}
	if f.gw.config.AI.Providers[0].TimeoutSeconds != 300 {
		t.Error("startup config was mutated in place")
	}
	got, err := f.gw.GetConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "GetConfiguration", got)
	assertNoSecret(t, "result", res)
	if strings.Contains(f.logs.String(), reloadSecret) {
		t.Error("logs leak the secret")
	}
	if !strings.Contains(f.logs.String(), "config update applied") {
		t.Errorf("no audit log line: %s", f.logs.String())
	}

	// File: placeholders, unknown and deprecated keys preserved; minimal diff;
	// mode 0600; backup holds the previous content.
	disk := f.file(t)
	want := strings.Replace(strings.Replace(f.orig,
		`"timeout_seconds": 300`, `"timeout_seconds": 600`, 1),
		`"max_concurrent": 2`, `"max_concurrent": 5`, 1)
	if disk != want {
		t.Errorf("config.json:\n%s\nwant:\n%s", disk, want)
	}
	if strings.Contains(disk, reloadSecret) {
		t.Error("expanded secret written to disk")
	}
	st, _ := os.Stat(f.path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	if b, err := os.ReadFile(res.BackupPath); err != nil || string(b) != f.orig {
		t.Errorf("backup %q = %q, %v", res.BackupPath, b, err)
	}
}

// callProvider makes one real call through the router so the provider's
// throttle pool exists.
func callProvider(t *testing.T, r *ai.Router, provider string) {
	t.Helper()
	if _, err := r.GenerateResponse(context.Background(), &sessions.Session{Key: "reload-test"}, "hi", provider); err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
}

func TestApplyConfigUpdate_PricingOverrideChangesCost(t *testing.T) {
	f := newReloadFixture(t)
	u := ai.Usage{PromptTokens: 1_000_000}
	if _, priced := f.router.TurnCost("z-ai", "custom-model-x", u); priced {
		t.Fatal("custom model unexpectedly priced before the override")
	}
	res, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
		"ai": map[string]interface{}{"pricing_overrides": map[string]interface{}{
			"custom-model-x": map[string]interface{}{"input_per_m_token": 2.0, "output_per_m_token": 4.0},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m := changeModes(res); m["ai.pricing_overrides.custom-model-x.input_per_m_token"] != toolstypes.ConfigChangeLive {
		t.Fatalf("modes = %v", m)
	}
	cost, priced := f.router.TurnCost("z-ai", "custom-model-x", u)
	if !priced || cost != 2.0 {
		t.Errorf("cost = %v priced=%v, want 2.0", cost, priced)
	}
	if ai.DefaultPricingResolver() != f.router.PricingResolver() {
		t.Error("package default resolver not updated")
	}
	t.Cleanup(func() { ai.SetDefaultPricingResolver(nil) })
}

func TestApplyConfigUpdate_InvalidChangeRejectedNothingChanged(t *testing.T) {
	for name, patch := range map[string]map[string]interface{}{
		"negative max_concurrent": {"ai.providers.z-ai.max_concurrent": -1.0},
		"type mismatch":           {"port": "not-a-number"},
		"port out of range":       {"port": 80.0},
		"unknown provider":        {"ai.providers.ghost.model": "x"},
		"literal secret":          {"ai.providers.z-ai.api_key": "sk-new-literal"},
		"redaction marker":        {"ai.providers.z-ai.model": config.RedactedValue},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReloadFixture(t)
			before, _ := f.router.GetProvider("z-ai")
			if _, err := f.gw.ApplyConfigUpdate(context.Background(), patch); err == nil {
				t.Fatal("expected rejection")
			}
			if f.file(t) != f.orig {
				t.Error("config.json changed")
			}
			if _, err := os.Stat(f.path + config.BackupSuffix); !os.IsNotExist(err) {
				t.Error("backup written for a rejected update")
			}
			if p, _ := f.router.GetProvider("z-ai"); p != before {
				t.Error("provider rebuilt")
			}
			if f.gw.reload.live.Load() != nil {
				t.Error("live config swapped")
			}
		})
	}
}

func TestApplyConfigUpdate_RestartOnlyFieldsSavedNotApplied(t *testing.T) {
	f := newReloadFixture(t)
	before, _ := f.router.GetProvider("cc")
	res, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
		"port":                         18800.0,
		"tools.sandbox.allowed_paths":  []interface{}{"/tmp", "/srv"},
		"ai.providers.cc.model":        "opus",
		"ai.providers.z-ai.max_tokens": nil, // absent key: no-op
	})
	if err != nil {
		t.Fatal(err)
	}
	modes := changeModes(res)
	for _, k := range []string{"port", "tools.sandbox.allowed_paths", "ai.providers.cc.model"} {
		if modes[k] != toolstypes.ConfigChangeRestart {
			t.Errorf("%s mode = %q, want requires_restart", k, modes[k])
		}
	}
	if len(res.Unchanged) != 1 {
		t.Errorf("unchanged = %v", res.Unchanged)
	}
	sec := map[string]bool{}
	for _, k := range res.SecurityKeys() {
		sec[k] = true
	}
	if !sec["tools.sandbox.allowed_paths"] || sec["port"] {
		t.Errorf("security keys = %v", res.SecurityKeys())
	}
	if f.gw.currentConfig().Port != 18789 {
		t.Error("restart-only port applied to the running config")
	}
	if p, _ := f.router.GetProvider("cc"); p != before {
		t.Error("claude-code provider rebuilt")
	}
	disk := f.file(t)
	if !strings.Contains(disk, `"port": 18800`) || !strings.Contains(disk, `"/srv"`) {
		t.Errorf("restart-only changes not saved:\n%s", disk)
	}
	if _, err := config.Load(f.path); err != nil {
		t.Errorf("saved config no longer loads: %v", err)
	}
}

func TestPlanConfigUpdate_ChangesNothing(t *testing.T) {
	f := newReloadFixture(t)
	res, err := f.gw.PlanConfigUpdate(context.Background(), map[string]interface{}{"ai.providers.z-ai.timeout_seconds": 900.0})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || len(res.Changes) != 1 || res.Changes[0].Mode != toolstypes.ConfigChangeLive {
		t.Fatalf("plan = %+v", res)
	}
	if f.file(t) != f.orig || f.gw.reload.live.Load() != nil {
		t.Fatal("plan changed state")
	}
}

func TestApplyConfigUpdate_UnavailableWithoutConfigPath(t *testing.T) {
	gw := &Gateway{config: config.Default()}
	if _, err := gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"port": 18800.0}); err == nil {
		t.Fatal("expected error without a config path")
	}
}

func TestApplyConfigUpdate_ConcurrentWithReaders(t *testing.T) {
	f := newReloadFixture(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = f.gw.GetConfiguration()
				_ = f.gw.getDefaultModel()
				_ = f.gw.getSubagentModel("")
				_ = f.router.DefaultModel()
				_, _ = f.router.TurnCost("z-ai", "glm-5.3", ai.Usage{PromptTokens: 10})
				_, _ = f.router.GenerateResponse(context.Background(), &sessions.Session{Key: "reload-race"}, "hi", "z-ai")
			}
		}()
	}
	for i := 0; i < 10; i++ {
		if _, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
			"ai.providers.z-ai.timeout_seconds": float64(300 + i + 1),
			"ai.providers.z-ai.max_concurrent":  float64(i + 1),
			"ai.subagent_default_model":         "glm-5.3-flash",
		}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if got := f.gw.getSubagentModel(""); got != "glm-5.3-flash" {
		t.Errorf("subagent model = %q", got)
	}
}

func TestApplyConfigUpdate_CallLogToggleAppliesLive(t *testing.T) {
	f := newReloadFixture(t)
	logPath := filepath.Join(t.TempDir(), "calls.jsonl")
	res, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
		"ai.call_log": map[string]interface{}{"enabled": true, "path": logPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m := changeModes(res); m["ai.call_log.enabled"] != toolstypes.ConfigChangeLive || m["ai.call_log.path"] != toolstypes.ConfigChangeLive {
		t.Fatalf("modes = %v", m)
	}
	cl := f.router.CallLog()
	if cl == nil || cl.Path() != logPath {
		t.Fatalf("call log = %v", cl)
	}
	if _, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"ai.call_log.enabled": false}); err != nil {
		t.Fatal(err)
	}
	if f.router.CallLog() != nil {
		t.Error("call log still attached after disabling it")
	}
}
