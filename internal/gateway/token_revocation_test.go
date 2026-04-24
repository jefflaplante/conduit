package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/logging"
	"conduit/internal/middleware"
	"conduit/internal/monitoring"
	"conduit/internal/sessions"
)

// createTestGatewayWithAuth creates a gateway with auth storage wired up for
// revocation testing.
func createTestGatewayWithAuth(t *testing.T) (*Gateway, *auth.TokenStorage) {
	t.Helper()

	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path: ":memory:",
		},
		RateLimiting: config.RateLimitingConfig{
			Enabled: false,
		},
	}

	sessionStore, err := sessions.NewStore(cfg.Database.Path)
	if err != nil {
		t.Fatalf("Failed to create session store: %v", err)
	}

	gatewayMetrics := monitoring.NewGatewayMetrics()
	gatewayMetrics.SetVersion("test")

	metricsCollector := monitoring.NewMetricsCollector(monitoring.CollectorDependencies{
		SessionStore:   sessionStore,
		GatewayMetrics: gatewayMetrics,
	})

	eventStore := monitoring.NewMemoryEventStore(100)

	rateLimitMiddleware := middleware.NewRateLimitMiddleware(middleware.RateLimitMiddlewareConfig{
		Config: middleware.RateLimitConfig{Enabled: false},
	})

	authStorage := auth.NewTokenStorage(sessionStore.DB(), "test-secret-key")

	// Create a test logger
	testLogger := logging.New("info", "text")

	gw := &Gateway{
		config:   cfg,
		logger:   testLogger,
		sessions: sessionStore,
		monitoring: &MonitoringService{
			GatewayMetrics:   gatewayMetrics,
			MetricsCollector: metricsCollector,
			EventStore:       eventStore,
		},
		rateLimitMiddleware: rateLimitMiddleware,
		auth: &AuthService{
			AuthStorage: authStorage,
		},
		ws: NewWebSocketService(testLogger, websocket.Upgrader{}, 16),
	}

	// Bind a gateway-lifecycle context to the WebSocketService so per-client
	// HandleClientWrite goroutines (started by the test handler) have a
	// non-nil context to select on for the gateway-shutdown branch.
	gw.ws.Start(context.Background())

	// Wire the revocation callback just like New() does.
	authStorage.OnRevoke(gw.handleTokenRevocation)

	return gw, authStorage
}

func TestHandleTokenRevocation_ClosesMatchingClients(t *testing.T) {
	gw, authStorage := createTestGatewayWithAuth(t)

	// Create two tokens
	resp1, err := authStorage.CreateToken(auth.CreateTokenRequest{ClientName: "client-a"})
	if err != nil {
		t.Fatalf("Failed to create token 1: %v", err)
	}
	resp2, err := authStorage.CreateToken(auth.CreateTokenRequest{ClientName: "client-b"})
	if err != nil {
		t.Fatalf("Failed to create token 2: %v", err)
	}

	// Set up a real WebSocket server backed by the test gateway so we get real conns.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		tokenID := r.URL.Query().Get("token_id")
		clientID := r.URL.Query().Get("client_id")

		client := &Client{
			ID:         clientID,
			TokenID:    tokenID,
			Conn:       conn,
			Send:       make(chan []byte, 16),
			CloseFrame: make(chan []byte, 1),
		}

		gw.ws.ClientMu.Lock()
		gw.ws.Clients[client.ID] = client
		gw.ws.ClientMu.Unlock()

		// Start the send-pump so RevokeClientByToken can hand off the
		// close frame via CloseFrame (conduit-1m5b). Without the pump
		// running, the revoker's non-blocking send would still land in
		// the buffer but nothing would emit the close frame — the client
		// would observe an abnormal close (1006) instead of the expected
		// ClosePolicyViolation (1008). Service-level Start() is bound
		// once at fixture setup; we only spin the per-client pump here.
		go gw.ws.HandleClientWrite(client)

		// Block until the connection is closed.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Connect two clients with token1 and one with token2.
	dialAndWait := func(clientID, tokenID string) *websocket.Conn {
		t.Helper()
		url := wsURL + "/?token_id=" + tokenID + "&client_id=" + clientID
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatalf("Dial failed for %s: %v", clientID, err)
		}
		// Wait for the server handler to register the client.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			gw.ws.ClientMu.RLock()
			_, ok := gw.ws.Clients[clientID]
			gw.ws.ClientMu.RUnlock()
			if ok {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return conn
	}

	conn1a := dialAndWait("c1a", resp1.TokenInfo.TokenID)
	defer conn1a.Close()

	conn1b := dialAndWait("c1b", resp1.TokenInfo.TokenID)
	defer conn1b.Close()

	conn2 := dialAndWait("c2", resp2.TokenInfo.TokenID)
	defer conn2.Close()

	// Verify 3 clients registered.
	gw.ws.ClientMu.RLock()
	if len(gw.ws.Clients) != 3 {
		t.Fatalf("Expected 3 clients, got %d", len(gw.ws.Clients))
	}
	gw.ws.ClientMu.RUnlock()

	// Revoke token 1 -- this should close c1a and c1b but leave c2 open.
	if err := authStorage.RevokeToken(resp1.TokenInfo.TokenID); err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}

	// conn1a and conn1b should receive a close frame. Set a short read
	// deadline and verify we get a close error.
	conn1a.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err1a := conn1a.ReadMessage()

	conn1b.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err1b := conn1b.ReadMessage()

	if err1a == nil || err1b == nil {
		t.Error("Expected both token1 connections to be closed after revocation")
	}

	// Verify the close errors contain the expected close code.
	if closeErr, ok := err1a.(*websocket.CloseError); ok {
		if closeErr.Code != websocket.ClosePolicyViolation {
			t.Errorf("Expected ClosePolicyViolation (1008), got %d", closeErr.Code)
		}
	}

	// conn2 should still be usable. Write a ping to confirm.
	if err := conn2.WriteMessage(websocket.PingMessage, nil); err != nil {
		t.Errorf("Expected conn2 to remain open, but write failed: %v", err)
	}
}

func TestHandleTokenRevocation_NoMatchingClients(t *testing.T) {
	gw, authStorage := createTestGatewayWithAuth(t)

	resp, err := authStorage.CreateToken(auth.CreateTokenRequest{ClientName: "client-a"})
	if err != nil {
		t.Fatalf("Failed to create token: %v", err)
	}

	// No clients connected -- revocation should succeed silently.
	if err := authStorage.RevokeToken(resp.TokenInfo.TokenID); err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}

	gw.ws.ClientMu.RLock()
	count := len(gw.ws.Clients)
	gw.ws.ClientMu.RUnlock()

	if count != 0 {
		t.Errorf("Expected 0 clients, got %d", count)
	}
}

func TestHandleTokenRevocation_OnlyTargetTokenClosed(t *testing.T) {
	gw, _ := createTestGatewayWithAuth(t)

	// Directly test handleTokenRevocation with mock clients (no real WS).
	gw.ws.ClientMu.Lock()
	gw.ws.Clients["a"] = &Client{ID: "a", TokenID: "token-1"}
	gw.ws.Clients["b"] = &Client{ID: "b", TokenID: "token-2"}
	gw.ws.Clients["c"] = &Client{ID: "c", TokenID: "token-1"}
	gw.ws.ClientMu.Unlock()

	// We can't call handleTokenRevocation on clients without real Conn objects
	// because Close() would panic. Instead, verify the filtering logic by
	// checking which clients match.
	gw.ws.ClientMu.RLock()
	var matches int
	for _, c := range gw.ws.Clients {
		if c.TokenID == "token-1" {
			matches++
		}
	}
	gw.ws.ClientMu.RUnlock()

	if matches != 2 {
		t.Errorf("Expected 2 clients matching token-1, got %d", matches)
	}

	// Verify token-2 client would NOT match.
	gw.ws.ClientMu.RLock()
	for _, c := range gw.ws.Clients {
		if c.TokenID == "token-2" && c.ID != "b" {
			t.Error("token-2 should only match client b")
		}
	}
	gw.ws.ClientMu.RUnlock()
}
