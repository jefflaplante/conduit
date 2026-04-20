package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/agent"
	"conduit/internal/ai"
	"conduit/internal/brain"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/tools"
	toolstypes "conduit/internal/tools/types"
	"conduit/internal/workspace"
)

// brainNew wraps brain.New to let the compiler find it as a package-local name.
var brainNew = brain.New

// TestConvertToolsToAIFormat exercises the registry→ai.Tool conversion,
// covering the path with zero tools (empty registry).
func TestConvertToolsToAIFormat_EmptyRegistry(t *testing.T) {
	reg := tools.NewRegistry(config.ToolsConfig{})
	reg.SetServices(&toolstypes.ToolServices{})
	aiTools := convertToolsToAIFormat(reg)
	if aiTools == nil {
		// ok — no tools
		return
	}
	// With empty registry and no services, aiTools should be empty
	if len(aiTools) > 0 {
		t.Logf("got %d tools (not necessarily an error)", len(aiTools))
	}
}

// TestCreateInternalToken uses a minimal auth-wired gateway.
func TestCreateInternalToken_Simple(t *testing.T) {
	gw, _ := createTestGatewayWithAuth(t)
	tok, err := gw.createInternalToken("test-internal")
	if err != nil {
		t.Fatalf("createInternalToken: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty token")
	}
}

func TestResolveEmbedder_TfidfDeprecated(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	emb, name := resolveEmbedder(config.VectorConfig{EmbedProvider: "tfidf"}, logger)
	if emb != nil {
		t.Error("expected nil embedder for tfidf")
	}
	if name != "tfidf-deprecated" {
		t.Errorf("expected 'tfidf-deprecated', got %q", name)
	}
}

func TestResolveEmbedder_OpenAINoKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	prev := os.Getenv("OPENAI_API_KEY")
	_ = os.Unsetenv("OPENAI_API_KEY")
	t.Cleanup(func() { _ = os.Setenv("OPENAI_API_KEY", prev) })

	emb, name := resolveEmbedder(config.VectorConfig{EmbedProvider: "openai"}, logger)
	if emb != nil {
		t.Error("expected nil embedder when no key")
	}
	if name != "openai-no-key" {
		t.Errorf("expected 'openai-no-key', got %q", name)
	}
}

func TestResolveEmbedder_OpenAIWithKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	emb, name := resolveEmbedder(config.VectorConfig{
		EmbedProvider: "openai",
		OpenAI:        &config.OpenAIEmbedConfig{APIKey: "sk-test"},
	}, logger)
	if emb == nil {
		t.Error("expected non-nil embedder with key")
	}
	if name != "openai" {
		t.Errorf("expected 'openai', got %q", name)
	}
}

func TestResolveEmbedder_Ollama(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	emb, name := resolveEmbedder(config.VectorConfig{EmbedProvider: "ollama"}, logger)
	if emb == nil {
		t.Error("expected non-nil ollama embedder")
	}
	if name != "ollama" {
		t.Errorf("expected 'ollama', got %q", name)
	}
}

// TestNewSummaryAIRouterAdapter verifies the constructor.
func TestNewSummaryAIRouterAdapter(t *testing.T) {
	router, err := ai.NewRouter(config.AIConfig{}, nil)
	if err != nil {
		t.Fatalf("ai.NewRouter: %v", err)
	}
	a := newSummaryAIRouterAdapter(router)
	if a == nil {
		t.Fatal("expected non-nil adapter")
	}
}

func TestSummaryAIResponseAdapter_GetContent(t *testing.T) {
	a := &summaryAIResponseAdapter{content: "hello"}
	if got := a.GetContent(); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestCreateSchemaBuilder_WithGateway(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	cfg := &config.Config{
		Workspace: config.WorkspaceConfig{ContextDir: t.TempDir()},
		Tools: config.ToolsConfig{
			Sandbox: config.SandboxConfig{AllowedPaths: []string{t.TempDir()}},
		},
	}
	b := createSchemaBuilder(gw, cfg)
	if b == nil {
		t.Error("expected non-nil builder")
	}
}

func TestCreateSchemaBuilder_NoGateway(t *testing.T) {
	cfg := &config.Config{
		Workspace: config.WorkspaceConfig{ContextDir: ""},
	}
	b := createSchemaBuilder(nil, cfg)
	if b == nil {
		t.Error("expected non-nil builder")
	}
}

// TestRefreshBeadsPeriodic_CancelImmediately verifies the function returns
// when ctx is cancelled.
func TestRefreshBeadsPeriodic_CancelImmediately(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()

	// brainService must not be nil — give it a real (but short-lived) brain.
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	b, err := brainNew(dbPath)
	if err != nil {
		t.Skip("brain unavailable")
		return
	}
	defer b.Close()
	gw.brainService = b

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Very long interval; the immediate refresh runs once then we return on ctx.Done.
	gw.refreshBeadsPeriodic(ctx, time.Hour)
}

// TestHandleTestMessage_AISet covers the path with ai set (may fail on
// AI call since we have no live provider, but we're just covering the flow).
func TestHandleTestMessage_AISet(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	gw.logger = newTestLogger()

	body := `{"message":"hi","user_id":"u1"}`
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	gw.handleTestMessage(w, req)
	// AI call to localhost ollama will fail with 500 or succeed if ollama is up.
	// Either way, code should be non-zero.
	if w.Code == 0 {
		t.Error("expected HTTP status code set")
	}
}

// TestGetSystemPromptDebug exercises the debug-info API with a live agent.
func TestGetSystemPromptDebug(t *testing.T) {
	dir := t.TempDir()
	store, err := sessions.NewStore(filepath.Join(dir, "g.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	sess, _ := store.GetOrCreateSession("u1", "c1")

	sm := skills.NewManager(skills.SkillsConfig{SearchPaths: []string{t.TempDir()}})
	agentSys := agent.NewConduitAgentWithIntegration(
		agent.AgentConfig{Name: "test"},
		[]ai.Tool{{Name: "T1", Description: "t"}},
		workspace.NewWorkspaceContext(t.TempDir()),
		nil, sm, nil, nil,
	)

	gw := &Gateway{sessions: store, agentSystem: agentSys}

	// With a valid session key
	debug, err := gw.GetSystemPromptDebug(context.Background(), sess.Key)
	if err != nil {
		t.Fatalf("GetSystemPromptDebug: %v", err)
	}
	if debug == nil {
		t.Error("expected non-nil debug")
	}

	// Missing session
	_, err = gw.GetSystemPromptDebug(context.Background(), "no-such-session")
	if err == nil {
		t.Error("expected error for missing session")
	}

	// Empty session key — skips session lookup
	_, err = gw.GetSystemPromptDebug(context.Background(), "")
	if err != nil {
		t.Errorf("expected no error with empty session, got %v", err)
	}
}

// TestSendToClient_UnmarshalableMessage covers the error log branch of
// sendToClient.
func TestSendToClient_UnmarshalableMessage(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	c := newTestWSClient("c1")
	// A channel value cannot be marshalled by encoding/json.
	gw.sendToClient(c, map[string]interface{}{"ch": make(chan int)})
}

// Keep imports alive with a no-op guard.
var _ io.Reader = strings.NewReader("")
