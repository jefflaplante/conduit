package gateway

import (
	"context"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
)

// newTestGatewayWithRouter returns a Gateway wired with a real (but empty)
// ai.Router and a provider metadata map so that commands that rely on the
// router (e.g. /provider, /model) can be exercised.
func newTestGatewayWithRouter(t *testing.T) (*Gateway, *sessions.Store, *ai.Router) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Build an AI router that has registered providers (ollama needs no API key)
	cfg := config.AIConfig{
		DefaultProvider: "testprov",
		Providers: []config.ProviderConfig{
			{
				Name:          "testprov",
				Type:          "ollama",
				Model:         "llama3",
				ContextWindow: 8000,
			},
		},
		ModelAliases: map[string]string{
			"sonnet": "claude-sonnet-4-20250514",
			"haiku":  "claude-haiku-4-5-20251001",
			"custom": "",
		},
	}
	router, err := ai.NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("ai.NewRouter: %v", err)
	}

	m := newTestChannelManager(t)
	gw := &Gateway{
		sessions:       store,
		channelManager: m,
		ai:             router,
		config:         &config.Config{AI: cfg},
	}
	return gw, store, router
}

func TestHandleProviderCommand_List(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	gw.handleProviderCommand(msg, "/provider", sess)
}

func TestHandleProviderCommand_Unknown(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	gw.handleProviderCommand(msg, "/provider nonexistent", sess)
}

func TestHandleProviderCommand_Switch(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	gw.handleProviderCommand(msg, "/provider testprov", sess)

	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["provider"] != "testprov" {
		t.Errorf("expected provider=testprov, got %q", refreshed.Context["provider"])
	}
	if refreshed.Context["model"] != "llama3" {
		t.Errorf("expected model=llama3 (default), got %q", refreshed.Context["model"])
	}
}

func TestHandleModelCommand_List(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	gw.handleModelCommand(msg, "/model", sess)
}

func TestHandleModelCommand_Reset(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")
	_ = store.SetSessionContext(sess.Key, "model", "claude-sonnet-4-20250514")
	_ = store.SetSessionContext(sess.Key, "provider", "anthropic")
	sess.Context["model"] = "claude-sonnet-4-20250514"
	sess.Context["provider"] = "anthropic"

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	gw.handleModelCommand(msg, "/model reset", sess)
	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["model"] != "" {
		t.Errorf("expected model cleared, got %q", refreshed.Context["model"])
	}
}

func TestHandleModelCommand_Default(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	gw.handleModelCommand(msg, "/model default", sess)
}

func TestHandleModelCommand_KnownAlias(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	gw.handleModelCommand(msg, "/model sonnet", sess)
	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("expected model=sonnet full, got %q", refreshed.Context["model"])
	}
}

func TestHandleModelCommand_EmptyAliasFallback(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	// "custom" is mapped to "" in aliases — should switch to default
	gw.handleModelCommand(msg, "/model custom", sess)
}

func TestHandleModelCommand_RawName(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	// Raw model name (contains '/' or len > 3) is accepted
	gw.handleModelCommand(msg, "/model claude-haiku-4-5-20251001", sess)
	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("expected model=claude-haiku-4-5-20251001, got %q", refreshed.Context["model"])
	}
}

func TestHandleModelCommand_UnknownShortAlias(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
	}
	// Short unknown alias (<= 3 chars, no '/') should be rejected
	gw.handleModelCommand(msg, "/model x", sess)
	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["model"] != "" {
		t.Errorf("expected model unchanged, got %q", refreshed.Context["model"])
	}
}

func TestFormatAliasDisplayWithProvider_Gateway(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	out := gw.formatAliasDisplayWithProvider(
		map[string]string{
			"sonnet":  "claude-sonnet-4-20250514",
			"default": "",
		},
		"• ", "→",
	)
	if out == "" {
		t.Error("expected non-empty output")
	}
}

func TestDirectClient_FormatAliasDisplayWithProvider(t *testing.T) {
	c, _ := newTestDirectClient(t)
	out := c.formatAliasDisplayWithProvider(
		map[string]string{
			"sonnet":  "claude-sonnet-4-20250514",
			"default": "",
		},
		"  ", "->",
	)
	if out == "" {
		t.Error("expected non-empty output")
	}
}

func TestDirectClient_GetContextWindow(t *testing.T) {
	// Need a DirectClient with a real router that has provider meta
	dbPath := t.TempDir() + "/dc.db"
	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.AIConfig{
		DefaultProvider: "testprov",
		Providers: []config.ProviderConfig{
			{Name: "testprov", Type: "ollama", Model: "llama3", ContextWindow: 16000},
		},
	}
	router, err := ai.NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("ai.NewRouter: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := NewDirectClient(DirectClientConfig{
		ParentCtx: ctx,
		UserID:    "jeff",
		Sessions:  store,
		AI:        router,
	})
	t.Cleanup(c.Close)

	// Explicit provider with configured ContextWindow
	if w := c.getContextWindow("testprov", ""); w != 16000 {
		t.Errorf("expected 16000, got %d", w)
	}
	// Empty provider, empty model -> falls back to default provider meta
	if w := c.getContextWindow("", ""); w != 16000 {
		t.Errorf("expected 16000, got %d", w)
	}
	// Empty provider, with model -> ResolveProviderForModel + meta lookup or model-based
	w := c.getContextWindow("", "claude-sonnet-4-20250514")
	if w <= 0 {
		t.Errorf("expected positive context window, got %d", w)
	}
}

func TestHandleCommand_Model(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
		Text:       "/model",
	}
	if !gw.handleCommand(context.Background(), msg, sess) {
		t.Error("/model should be handled")
	}
}

func TestHandleCommand_Provider(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
		Text:       "/provider",
	}
	if !gw.handleCommand(context.Background(), msg, sess) {
		t.Error("/provider should be handled")
	}
}

func TestHandleCommand_Cost(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: sess.Key,
		Text:       "/cost",
	}
	if !gw.handleCommand(context.Background(), msg, sess) {
		t.Error("/cost should be handled")
	}
}
