package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/workspace"
)

func TestDeriveRuntimeChannel_Defaults(t *testing.T) {
	got := deriveRuntimeChannel(nil)
	if got != "websocket" {
		t.Errorf("expected websocket, got %q", got)
	}

	got = deriveRuntimeChannel([]config.ChannelConfig{
		{Type: "telegram", Enabled: false},
	})
	if got != "websocket" {
		t.Errorf("expected websocket when all disabled, got %q", got)
	}
}

func TestDeriveRuntimeChannel_EnabledChannel(t *testing.T) {
	got := deriveRuntimeChannel([]config.ChannelConfig{
		{Type: "telegram", Enabled: false},
		{Type: "discord", Enabled: true},
	})
	if got != "discord" {
		t.Errorf("expected discord, got %q", got)
	}

	got = deriveRuntimeChannel([]config.ChannelConfig{
		{Type: "telegram", Enabled: true},
	})
	if got != "telegram" {
		t.Errorf("expected telegram, got %q", got)
	}
}

func TestOllamaConfigValues_Defaults(t *testing.T) {
	// Save + restore env to avoid leaking state
	prev := os.Getenv("OLLAMA_HOST")
	_ = os.Unsetenv("OLLAMA_HOST")
	t.Cleanup(func() { _ = os.Setenv("OLLAMA_HOST", prev) })

	host, model := ollamaConfigValues(config.VectorConfig{})
	if host != defaultOllamaEmbedHost {
		t.Errorf("expected default host, got %q", host)
	}
	if model != defaultOllamaEmbedModel {
		t.Errorf("expected default model, got %q", model)
	}
}

func TestOllamaConfigValues_FromConfig(t *testing.T) {
	prev := os.Getenv("OLLAMA_HOST")
	_ = os.Unsetenv("OLLAMA_HOST")
	t.Cleanup(func() { _ = os.Setenv("OLLAMA_HOST", prev) })

	cfg := config.VectorConfig{
		Ollama: &config.OllamaEmbedConfig{
			Host:  "http://ollama-host:11434",
			Model: "custom-model",
		},
	}
	host, model := ollamaConfigValues(cfg)
	if host != "http://ollama-host:11434" {
		t.Errorf("expected host from config, got %q", host)
	}
	if model != "custom-model" {
		t.Errorf("expected model from config, got %q", model)
	}
}

func TestOllamaConfigValues_EnvOverride(t *testing.T) {
	prev := os.Getenv("OLLAMA_HOST")
	_ = os.Setenv("OLLAMA_HOST", "http://env-host:9999")
	t.Cleanup(func() { _ = os.Setenv("OLLAMA_HOST", prev) })

	host, _ := ollamaConfigValues(config.VectorConfig{})
	if host != "http://env-host:9999" {
		t.Errorf("expected env host override, got %q", host)
	}
}

func TestShouldAutoEnableVecgo_ExplicitProvider(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	if !shouldAutoEnableVecgo(config.VectorConfig{EmbedProvider: "openai"}, logger) {
		t.Error("expected auto-enable when provider set to openai")
	}
	if !shouldAutoEnableVecgo(config.VectorConfig{EmbedProvider: "ollama"}, logger) {
		t.Error("expected auto-enable when provider set to ollama")
	}
}

func TestShouldAutoEnableVecgo_OpenAIKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	prev := os.Getenv("OPENAI_API_KEY")
	prevOll := os.Getenv("OLLAMA_HOST")
	_ = os.Setenv("OPENAI_API_KEY", "sk-test")
	_ = os.Unsetenv("OLLAMA_HOST")
	t.Cleanup(func() {
		_ = os.Setenv("OPENAI_API_KEY", prev)
		_ = os.Setenv("OLLAMA_HOST", prevOll)
	})

	if !shouldAutoEnableVecgo(config.VectorConfig{}, logger) {
		t.Error("expected auto-enable when OPENAI_API_KEY is set")
	}
}

func TestConvertSummaryFileConfigs_Empty(t *testing.T) {
	if got := convertSummaryFileConfigs(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if got := convertSummaryFileConfigs(map[string]config.SummaryFileConfig{}); got != nil {
		t.Errorf("expected nil for empty, got %v", got)
	}
}

func TestConvertSummaryFileConfigs_Populated(t *testing.T) {
	in := map[string]config.SummaryFileConfig{
		"notes.md": {Ratio: 0.5, PreserveKeys: []string{"header"}},
		"bug.md":   {Ratio: 0.25, PreserveKeys: []string{"title"}},
	}
	out := convertSummaryFileConfigs(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(out))
	}
	if out["notes.md"].Ratio != 0.5 {
		t.Errorf("unexpected notes ratio: %v", out["notes.md"].Ratio)
	}
	if len(out["bug.md"].PreserveKeys) != 1 || out["bug.md"].PreserveKeys[0] != "title" {
		t.Errorf("unexpected preserve keys: %v", out["bug.md"].PreserveKeys)
	}
	_ = workspace.SummaryFileConfig{} // ensure import retained
}

func TestGatewayShutdownManager_Getter(t *testing.T) {
	gw := &Gateway{}
	if gw.ShutdownManager() != nil {
		t.Error("expected nil shutdown manager on bare gateway")
	}
	// With a ShutdownManager assigned
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sm := NewShutdownManager(logger, &Gateway{
		clients:        map[string]*Client{},
		activeRequests: map[string]context.CancelFunc{},
		config:         &config.Config{DataDir: t.TempDir()},
	})
	gw.shutdownMgr = sm
	if gw.ShutdownManager() != sm {
		t.Error("expected ShutdownManager getter to return stored sm")
	}
}

// TestProcessRestartBreadcrumb_Missing verifies processRestartBreadcrumb is a
// no-op when the breadcrumb file doesn't exist.
func TestProcessRestartBreadcrumb_Missing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "g.db")
	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	gw := &Gateway{
		sessions: store,
		logger:   logger,
		config:   &config.Config{DataDir: dir},
	}
	// No breadcrumb file -> should be a no-op
	gw.processRestartBreadcrumb()
}

func TestProcessRestartBreadcrumb_Malformed(t *testing.T) {
	dir := t.TempDir()
	bc := filepath.Join(dir, ".conduit-restart.json")
	if err := os.WriteFile(bc, []byte("not json"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := sessions.NewStore(filepath.Join(dir, "g.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	gw := &Gateway{
		sessions: store,
		logger:   logger,
		config:   &config.Config{DataDir: dir},
	}
	gw.processRestartBreadcrumb()
	// Breadcrumb file should have been removed
	if _, err := os.Stat(bc); !os.IsNotExist(err) {
		t.Error("expected breadcrumb file to be removed after parse failure")
	}
}

func TestProcessRestartBreadcrumb_RestoresSessions(t *testing.T) {
	dir := t.TempDir()
	store, err := sessions.NewStore(filepath.Join(dir, "g.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")

	// Build breadcrumb referencing this session
	bc := RestartBreadcrumb{
		Timestamp: time.Now(),
		Reason:    "test-restart",
		ActiveSessions: []BreadcrumbSession{
			{SessionKey: sess.Key, UserID: "jeff", ChannelID: "tui_jeff"},
			{SessionKey: "nonexistent", UserID: "x", ChannelID: "y"},
		},
	}
	data, _ := json.Marshal(bc)
	if err := os.WriteFile(filepath.Join(dir, ".conduit-restart.json"), data, 0644); err != nil {
		t.Fatalf("write breadcrumb: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gw := &Gateway{
		sessions: store,
		logger:   logger,
		config:   &config.Config{DataDir: dir},
	}
	gw.processRestartBreadcrumb()

	// Session should have received a resume message
	messages, _ := store.GetMessages(sess.Key, 10)
	if len(messages) == 0 {
		t.Fatal("expected resume message added to session")
	}
	found := false
	for _, m := range messages {
		if m.Role == "assistant" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected assistant resume message")
	}
}

func TestGetDefaultModel_NoConfig(t *testing.T) {
	gw := &Gateway{}
	if m := gw.getDefaultModel(); m != "claude-sonnet-4-20250514" {
		t.Errorf("expected fallback, got %q", m)
	}
}

func TestGetDefaultModel_EmptyProviders(t *testing.T) {
	gw := &Gateway{config: &config.Config{AI: config.AIConfig{Providers: nil}}}
	if m := gw.getDefaultModel(); m != "claude-sonnet-4-20250514" {
		t.Errorf("expected fallback, got %q", m)
	}
}

func TestGetDefaultModel_FromDefaultProvider(t *testing.T) {
	gw := &Gateway{config: &config.Config{AI: config.AIConfig{
		DefaultProvider: "primary",
		Providers: []config.ProviderConfig{
			{Name: "fallback", Type: "anthropic", Model: "claude-haiku-4-5-20251001"},
			{Name: "primary", Type: "anthropic", Model: "claude-sonnet-4-20250514"},
		},
	}}}
	if m := gw.getDefaultModel(); m != "claude-sonnet-4-20250514" {
		t.Errorf("expected sonnet from primary provider, got %q", m)
	}
}

func TestGetDefaultModel_FromFirstProvider(t *testing.T) {
	// When default_provider doesn't match, fall back to first provider
	gw := &Gateway{config: &config.Config{AI: config.AIConfig{
		DefaultProvider: "unknown",
		Providers: []config.ProviderConfig{
			{Name: "first", Type: "anthropic", Model: "first-model"},
			{Name: "second", Type: "anthropic", Model: "second-model"},
		},
	}}}
	if m := gw.getDefaultModel(); m != "first-model" {
		t.Errorf("expected first-model, got %q", m)
	}
}
