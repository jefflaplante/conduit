package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "conduit/internal/searchdb" // sqlite driver side-effect import
	"github.com/gorilla/websocket"

	"conduit/internal/ai"
	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/database"
	"conduit/internal/middleware"
	"conduit/internal/sessions"
)

// failingProvider is a test provider whose GenerateResponse fails after a
// configurable delay, simulating a provider that dies mid-turn (bd-tgy6).
type failingProvider struct {
	name  string
	delay time.Duration
}

func (f *failingProvider) Name() string { return f.name }

func (f *failingProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(f.delay):
		return nil, fmt.Errorf("post «context deadline exceeded» simulated provider failure (bd-tgy6)")
	}
}

// newTestGatewayWithFailingProvider builds a gateway whose turn runner is
// wired to a router with a provider that always fails mid-flight.
func newTestGatewayWithFailingProvider(t *testing.T, delay time.Duration) (*Gateway, *sessions.Store, *failingProvider) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.AIConfig{
		DefaultProvider: "failing",
		Providers: []config.ProviderConfig{
			{Name: "failing", Type: "ollama", Model: "mock-model", ContextWindow: 8000},
		},
	}
	router, err := ai.NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("ai.NewRouter: %v", err)
	}
	provider := &failingProvider{name: "failing", delay: delay}
	router.RegisterProvider("failing", provider)

	m := newTestChannelManager(t)
	testLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gw := &Gateway{
		sessions:       store,
		channelManager: m,
		ai:             router,
		logger:         testLogger,
		ws:             NewWebSocketService(testLogger, websocket.Upgrader{}, 16),
		config:         &config.Config{AI: cfg},
	}
	return gw, store, provider
}

// newLocalListener binds 127.0.0.1:0 for httptest servers built from an
// existing *http.Server (so its timeouts are preserved).
func newLocalListener() net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	return ln
}

// postTestMessage drives handleTestMessage the way a wake script does.
func postTestMessage(t *testing.T, gw *Gateway, message string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"message":%q,"user_id":"u1"}`, message)
	req := httptest.NewRequest(http.MethodPost, "/api/test/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	gw.handleTestMessage(w, req)
	return w
}

// bd-tgy6: when the LLM provider fails mid-turn (e.g. "context deadline
// exceeded"), the HTTP handler must return a JSON error response with a
// distinguishable error code/message instead of closing the connection
// silently (which the client observes as curl 52 "Empty reply from server").
func TestHandleTestMessage_ProviderErrorReturnsJSONError(t *testing.T) {
	gw, _, _ := newTestGatewayWithFailingProvider(t, 0)
	w := postTestMessage(t, gw, "wake the agent")

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway for provider failure, got %d (body=%q)", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json Content-Type, got %q", ct)
	}

	var parsed struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Details string `json:"details"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("response body is not valid JSON: %v (body=%q)", err, w.Body.String())
	}
	if parsed.Error == "" {
		t.Error("expected non-empty 'error' field in JSON body")
	}
	if parsed.Code != "provider_error" {
		t.Errorf("expected code=provider_error, got %q", parsed.Code)
	}
	if parsed.Details == "" {
		t.Errorf("expected non-empty 'details' field carrying the underlying provider error, body=%q", w.Body.String())
	}
	if !strings.Contains(parsed.Details, "context deadline exceeded") {
		t.Errorf("expected details to carry the provider error text, got %q", parsed.Details)
	}
}

// bd-tgy6: a turn that never ran because its context was already cancelled
// (client hung up, /stop, shutdown drain) is not a provider failure — the
// handler must not present it as one.
func TestHandleTestMessage_CancelledTurnIsNotProviderError(t *testing.T) {
	gw, _, _ := newTestGatewayWithFailingProvider(t, 0)

	body := `{"message":"wake the agent","user_id":"u1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/test/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Pre-cancelled request context: the turn is dropped before it starts.
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	*req = *req.WithContext(ctx)

	w := httptest.NewRecorder()
	gw.handleTestMessage(w, req)

	if w.Code == 0 {
		t.Fatal("no status written")
	}
	if w.Code == http.StatusBadGateway {
		t.Fatalf("dropped/cancelled turn must not be reported as provider_error (got 502, body=%q)", w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("expected JSON content type, got %q body=%q", w.Header().Get("Content-Type"), w.Body.String())
	}
	var parsed struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("body not JSON: %v (body=%q)", err, w.Body.String())
	}
	if parsed.Error == "" {
		t.Errorf("expected 'error' field, body=%q", w.Body.String())
	}
	if parsed.Code != "turn_dropped" && parsed.Code != "turn_cancelled" {
		t.Errorf("expected code=turn_dropped or turn_cancelled, got %q (body=%q)", parsed.Code, w.Body.String())
	}
}

// TestHandleTestMessage_LongTurnSurvivesWriteTimeout drives the full HTTP
// stack (buildHTTPServer) with a WriteTimeout SHORTER than the provider
// failure delay. This reproduces bd-tgy6 exactly: without the write-deadline
// extension, the server closes the connection mid-turn and the client gets
// an empty reply (curl 52); with it, the client receives the JSON error.
func TestHandleTestMessage_LongTurnSurvivesWriteTimeout(t *testing.T) {
	gw, _, _ := newTestGatewayWithFailingProvider(t, 2*time.Second)

	// Full-stack wiring: auth + rate limiter, as in production.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open auth db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatalf("configure auth db: %v", err)
	}
	if err := database.RunMigrations(db); err != nil {
		t.Fatalf("migrate auth db: %v", err)
	}
	authSvc, err := NewAuthService(&config.Config{}, gw.logger, db)
	if err != nil {
		t.Fatalf("NewAuthService: %v", err)
	}
	gw.auth = authSvc
	gw.rateLimitMiddleware = middleware.NewRateLimitMiddleware(middleware.RateLimitMiddlewareConfig{})
	gw.search = &SearchService{}

	// Mint a token so the request passes the auth middleware.
	tokenResp, err := authSvc.AuthStorage.CreateToken(auth.CreateTokenRequest{ClientName: "wake-script"})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	server := gw.buildHTTPServer()
	// Use the production server object itself so its timeouts apply —
	// httptest.NewServer would silently build a fresh server with none.
	// 1s WriteTimeout vs 2s provider delay: without the extension the
	// write deadline fires mid-turn (the reported curl-52 mechanism).
	server.WriteTimeout = 1 * time.Second
	ts := &httptest.Server{
		Listener: newLocalListener(),
		Config:   server,
	}
	ts.Start()
	defer ts.Close()

	body := `{"message":"wake the agent","user_id":"u1"}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/test/message", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenResp.Token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed (client saw empty reply/connection close?): %v", err)
	}
	defer resp.Body.Close()

	respBody := new(bytes.Buffer)
	_, _ = respBody.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%q", resp.StatusCode, respBody.String())
	}
	if !strings.Contains(respBody.String(), "provider_error") {
		t.Errorf("expected code=provider_error in body, got %q", respBody.String())
	}
}
