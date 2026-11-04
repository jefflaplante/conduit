package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/middleware"
)

// H2: revocation, role changes and expiry recorded in token storage reach
// live WebSocket connections. The tests change tokens through a second
// TokenStorage on the same DB, like the `conduit token` CLI in another
// process, so the gateway's in-process OnRevoke hook never fires.

const testRecheckInterval = 40 * time.Millisecond

// recheckFixture serves WS connections whose Client carries the token's ID
// and role, with the real pump and the gateway's token re-check.
type recheckFixture struct {
	gw    *Gateway
	cli   *auth.TokenStorage // the "CLI process"
	wsURL string
}

// newAuthGateway is a gateway over a file DB (an in-memory DB is one DB per
// pool connection, so concurrent re-checks would see empty schemas) with
// token storage wired and the WS service started.
func newAuthGateway(t *testing.T) (*Gateway, *auth.TokenStorage) {
	t.Helper()
	gw, store := newTestGatewayWithSessions(t)
	storage := auth.NewTokenStorage(store.DB(), "test-secret-key")
	gw.auth = &AuthService{AuthStorage: storage}
	gw.ws.Start(context.Background())
	return gw, storage
}

func newRecheckFixture(t *testing.T) *recheckFixture {
	t.Helper()
	gw, _ := newAuthGateway(t)
	gw.ws.TokenRecheckInterval = testRecheckInterval
	gw.ws.TokenCheck = gw.wsTokenCheck

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		q := r.URL.Query()
		client := &Client{
			ID:         q.Get("client_id"),
			Role:       q.Get("client_id"),
			TokenID:    q.Get("token_id"),
			Owner:      q.Get("owner") == "1",
			Conn:       conn,
			Send:       make(chan []byte, 16),
			CloseFrame: make(chan []byte, 1),
		}
		gw.ws.ClientMu.Lock()
		gw.ws.Clients[client.ID] = client
		gw.ws.ClientMu.Unlock()
		go gw.ws.HandleClientWrite(client)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return &recheckFixture{
		gw:    gw,
		cli:   auth.NewTokenStorage(gw.sessions.DB(), "test-secret-key"),
		wsURL: "ws" + strings.TrimPrefix(srv.URL, "http"),
	}
}

// token creates a token with role (and optional expiry) via the CLI store.
func (f *recheckFixture) token(t *testing.T, name, role string, expires *time.Time) string {
	t.Helper()
	resp, err := f.cli.CreateToken(auth.CreateTokenRequest{
		ClientName: name, ExpiresAt: expires,
		Metadata: map[string]string{auth.MetadataKeyRole: role},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.TokenInfo.TokenID
}

func (f *recheckFixture) dial(t *testing.T, clientID, tokenID string, owner bool) *websocket.Conn {
	t.Helper()
	o := "0"
	if owner {
		o = "1"
	}
	conn, _, err := websocket.DefaultDialer.Dial(f.wsURL+"/?client_id="+clientID+"&token_id="+tokenID+"&owner="+o, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// expectClosed reads until the server's close frame and checks it is 4403
// with reason.
func expectClosed(t *testing.T, conn *websocket.Conn, reason string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var ce *websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("connection not closed by the server within 2s: %v", err)
		}
		if ce.Code != middleware.CloseForbidden || ce.Text != reason {
			t.Fatalf("close = %d %q, want %d %q", ce.Code, ce.Text, middleware.CloseForbidden, reason)
		}
		return
	}
}

// expectOpen waits several re-check intervals and then proves the
// connection and its pump are alive by receiving a message through Send.
func (f *recheckFixture) expectOpen(t *testing.T, conn *websocket.Conn, clientID string) {
	t.Helper()
	time.Sleep(6 * testRecheckInterval)
	f.gw.ws.ClientMu.RLock()
	c := f.gw.ws.Clients[clientID]
	f.gw.ws.ClientMu.RUnlock()
	if c == nil {
		t.Fatal("client not registered")
	}
	c.Send <- []byte(`{"type":"ping-test"}`)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(msg), "ping-test") {
		t.Fatalf("valid connection was not kept: %q, %v", msg, err)
	}
}

func TestWSTokenRecheck_RevokedInStorageCloses(t *testing.T) {
	f := newRecheckFixture(t)
	id := f.token(t, "script", auth.RoleOwner, nil)
	conn := f.dial(t, "c1", id, true)
	if err := f.cli.RevokeToken(id); err != nil { // no OnRevoke in this store
		t.Fatal(err)
	}
	expectClosed(t, conn, "token revoked")
}

func TestWSTokenRecheck_DeletedTokenCloses(t *testing.T) {
	f := newRecheckFixture(t)
	id := f.token(t, "script", auth.RoleAutomation, nil)
	conn := f.dial(t, "c1", id, false)
	if err := f.cli.DeleteToken(id); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, conn, "token check failed")
}

func TestWSTokenRecheck_ExpiredCloses(t *testing.T) {
	f := newRecheckFixture(t)
	exp := time.Now().Add(300 * time.Millisecond)
	id := f.token(t, "script", auth.RoleOwner, &exp)
	conn := f.dial(t, "c1", id, true)
	expectClosed(t, conn, "token expired")
}

func TestWSTokenRecheck_RoleChangeCloses(t *testing.T) {
	for _, tt := range []struct {
		name     string
		from, to string
	}{
		{"owner demoted to automation", auth.RoleOwner, auth.RoleAutomation},
		{"automation promoted to owner", auth.RoleAutomation, auth.RoleOwner},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newRecheckFixture(t)
			id := f.token(t, "script", tt.from, nil)
			conn := f.dial(t, "c1", id, tt.from == auth.RoleOwner)
			if err := f.cli.SetTokenRole(id, tt.to); err != nil {
				t.Fatal(err)
			}
			expectClosed(t, conn, "token role changed")
		})
	}
}

// An untagged token is an owner (auth.DefaultRole); tagging it owner
// explicitly changes nothing.
func TestWSTokenRecheck_ValidTokensStayConnected(t *testing.T) {
	f := newRecheckFixture(t)
	owner := f.token(t, "me", auth.RoleOwner, nil)
	future := time.Now().Add(time.Hour)
	bot := f.token(t, "bot", auth.RoleAutomation, &future)
	untagged, err := f.cli.CreateToken(auth.CreateTokenRequest{ClientName: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	connOwner := f.dial(t, "owner", owner, true)
	connBot := f.dial(t, "bot", bot, false)
	connLegacy := f.dial(t, "legacy", untagged.TokenInfo.TokenID, true)
	if err := f.cli.SetTokenRole(untagged.TokenInfo.TokenID, auth.RoleOwner); err != nil {
		t.Fatal(err)
	}
	f.expectOpen(t, connOwner, "owner")
	f.expectOpen(t, connBot, "bot")
	f.expectOpen(t, connLegacy, "legacy")

	// Revoking one token leaves the others alone.
	if err := f.cli.RevokeToken(bot); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, connBot, "token revoked")
	f.expectOpen(t, connOwner, "owner")
}

// Fail closed: a connection whose token cannot be checked is dropped.
func TestWSTokenCheck_FailsClosed(t *testing.T) {
	gw, storage := newAuthGateway(t)
	if err := gw.wsTokenCheck(&Client{ID: "x"}); err == nil {
		t.Error("client without a token ID passed the check")
	}
	if err := gw.wsTokenCheck(&Client{ID: "x", TokenID: "no-such-token"}); err == nil {
		t.Error("unknown token passed the check")
	}
	resp, err := storage.CreateToken(auth.CreateTokenRequest{ClientName: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Gateway{logger: gw.logger}).wsTokenCheck(&Client{ID: "x", TokenID: resp.TokenInfo.TokenID, Owner: true}); err == nil {
		t.Error("check without token storage passed")
	}
	if err := gw.wsTokenCheck(&Client{ID: "x", TokenID: resp.TokenInfo.TokenID, Owner: true}); err != nil {
		t.Errorf("valid untagged owner token failed: %v", err)
	}
}

// New wires the re-check into the WebSocket service.
func TestNew_WiresWSTokenRecheck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", dir)
	cfg := config.Default()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cfg.DataDir = dir
	cfg.Database.Path = filepath.Join(dir, "gw.db")
	cfg.AI.Providers = []config.ProviderConfig{{Name: "anthropic", Type: "anthropic", APIKey: "test-key", Model: "claude-sonnet-4-20250514"}}
	cfg.Tools.Sandbox.WorkspaceDir = filepath.Join(dir, "workspace")
	cfg.Tools.Sandbox.AllowedPaths = []string{cfg.Tools.Sandbox.WorkspaceDir}
	cfg.Channels = nil
	cfg.Heartbeat.Enabled = false
	cfg.Logging.Level = "error"

	gw, err := New(cfg)
	if err != nil {
		t.Skipf("cannot construct gateway in this environment: %v", err)
	}
	if gw.ws.TokenCheck == nil {
		t.Fatal("New did not wire the WebSocket token re-check")
	}
	if gw.ws.tokenRecheckInterval() != defaultWSTokenRecheck {
		t.Fatalf("default re-check interval = %v", gw.ws.tokenRecheckInterval())
	}
}
