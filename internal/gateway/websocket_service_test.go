package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsServiceTestServer spins up a real HTTP/WebSocket server that upgrades
// each connection, wires a *Client (Send + CloseFrame allocated, Conn set),
// registers it on the service map, and spawns HandleClientWrite on the
// server goroutine. The caller drives traffic via the returned dialer and
// triggers revocation via svc.RevokeClientByToken.
//
// This mirrors the real wiring in Gateway.handleWebSocket so we exercise
// the same goroutine topology the production bug manifested in.
func wsServiceTestServer(t *testing.T) (*WebSocketService, string, func()) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := NewWebSocketService(logger, websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}, 16)
	// Bind a gateway-lifecycle context so HandleClientWrite's ctx.Done case
	// doesn't fire during the test (we want revocation to drive shutdown).
	svc.Start(context.Background())

	var pumpWG sync.WaitGroup

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := svc.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		tokenID := r.URL.Query().Get("token_id")
		clientID := r.URL.Query().Get("client_id")
		client := &Client{
			ID:         clientID,
			TokenID:    tokenID,
			Conn:       conn,
			Send:       make(chan []byte, 64),
			CloseFrame: make(chan []byte, 1),
		}
		svc.ClientMu.Lock()
		svc.Clients[clientID] = client
		svc.ClientMu.Unlock()

		pumpWG.Add(1)
		go func() {
			defer pumpWG.Done()
			svc.HandleClientWrite(client)
		}()

		// Blocking reader: keep the connection alive until the client or
		// revocation tears it down. Discards any client-originated frames.
		for {
			if _, _, rerr := conn.ReadMessage(); rerr != nil {
				return
			}
		}
	}))

	cleanup := func() {
		srv.Close()
		// Give pump goroutines a moment to exit after server tear-down.
		done := make(chan struct{})
		go func() {
			pumpWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("pump goroutines did not exit within timeout after server shutdown")
		}
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return svc, wsURL, cleanup
}

// TestRevokeClientByToken_DoesNotRaceWithWrites is the conduit-1m5b
// regression test. With -race enabled, it spawns N writers hammering
// client.Send on the pump goroutine while concurrently calling
// RevokeClientByToken from a separate goroutine. Prior to the fix this
// produced a data race on the gorilla websocket writer state (two
// goroutines invoking Conn.WriteControl + Conn.WriteMessage concurrently)
// and occasionally a panic from the underlying buffered writer.
//
// The post-fix invariant is that all Conn.Write* calls happen on the
// pump goroutine, driven by Send / CloseFrame / ctx.Done selects. The
// revoker hands off a close payload via CloseFrame (non-blocking).
func TestRevokeClientByToken_DoesNotRaceWithWrites(t *testing.T) {
	svc, wsURL, cleanup := wsServiceTestServer(t)
	defer cleanup()

	const numClients = 4
	const tokenID = "race-token"

	type conn struct {
		c  *websocket.Conn
		id string
	}
	conns := make([]conn, 0, numClients)
	for i := 0; i < numClients; i++ {
		id := "client-" + string(rune('a'+i))
		url := wsURL + "/?token_id=" + tokenID + "&client_id=" + id
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", id, err)
		}
		conns = append(conns, conn{c: c, id: id})
	}
	defer func() {
		for _, cc := range conns {
			_ = cc.c.Close()
		}
	}()

	// Wait for all pumps to be registered on the service.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.ClientMu.RLock()
		n := len(svc.Clients)
		svc.ClientMu.RUnlock()
		if n == numClients {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	svc.ClientMu.RLock()
	if got := len(svc.Clients); got != numClients {
		svc.ClientMu.RUnlock()
		t.Fatalf("expected %d clients registered, got %d", numClients, got)
	}
	svc.ClientMu.RUnlock()

	// Grab client pointers so writer goroutines don't race against the
	// service map with the revoker (the revoker mutates the Clients map
	// observation via the RLock, but that's unrelated to the bug).
	svc.ClientMu.RLock()
	clients := make([]*Client, 0, numClients)
	for _, c := range svc.Clients {
		clients = append(clients, c)
	}
	svc.ClientMu.RUnlock()

	// Producers: flood each client.Send with messages for 50ms. The pump
	// will issue Conn.WriteMessage(TextMessage, ...) for each. We use a
	// context to stop producers cleanly regardless of revoke timing.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var producerWG sync.WaitGroup
	for _, cl := range clients {
		cl := cl
		producerWG.Add(1)
		go func() {
			defer producerWG.Done()
			payload := []byte(`{"type":"noise"}`)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				select {
				case cl.Send <- payload:
				case <-ctx.Done():
					return
				default:
					// Send buffer full — yield and retry.
					time.Sleep(time.Microsecond)
				}
			}
		}()
	}

	// Kick revoke on a separate goroutine after a short delay so writes
	// are genuinely in flight when the close frame is queued.
	time.Sleep(5 * time.Millisecond)
	revokedCount := svc.RevokeClientByToken(tokenID)
	if revokedCount != numClients {
		t.Errorf("RevokeClientByToken returned %d, want %d", revokedCount, numClients)
	}

	// Client side: drain each dialer until we see a close frame. The
	// pump must deliver ClosePolicyViolation (1008) with reason "token
	// revoked" before the connection tears down.
	var closedOK atomic.Int32
	var seenPolicyViolation atomic.Int32
	var clientWG sync.WaitGroup
	for _, cc := range conns {
		cc := cc
		clientWG.Add(1)
		go func() {
			defer clientWG.Done()
			_ = cc.c.SetReadDeadline(time.Now().Add(3 * time.Second))
			for {
				_, _, err := cc.c.ReadMessage()
				if err == nil {
					continue
				}
				closedOK.Add(1)
				if ce, ok := err.(*websocket.CloseError); ok {
					if ce.Code == websocket.ClosePolicyViolation && ce.Text == "token revoked" {
						seenPolicyViolation.Add(1)
					}
				}
				return
			}
		}()
	}

	clientWG.Wait()
	cancel()
	producerWG.Wait()

	if closedOK.Load() != numClients {
		t.Errorf("expected %d closed clients, got %d", numClients, closedOK.Load())
	}
	if seenPolicyViolation.Load() == 0 {
		t.Errorf("expected at least one client to receive ClosePolicyViolation/token revoked close frame, got 0")
	}
}

// TestRevokeClientByToken_NilCloseFrameFallsBackToConnClose verifies the
// defensive branch: if a caller constructed a Client without allocating
// CloseFrame (e.g. legacy test fixtures or partially-initialized stubs),
// the revoker must still tear down the conn via Conn.Close rather than
// panic on nil-channel send.
func TestRevokeClientByToken_NilCloseFrameFallsBackToConnClose(t *testing.T) {
	svc := newTestWSService()

	// Stand up a single real websocket connection but do NOT start a pump
	// or allocate CloseFrame on the server-side Client. The revoker must
	// see CloseFrame == nil and fall back to Conn.Close().
	var serverConn *websocket.Conn
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConn = c
		close(ready)
		// Block on read so the conn stays alive until revoke closes it.
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	cc, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()

	<-ready

	// Register a Client with Send allocated but CloseFrame nil.
	client := &Client{
		ID:      "nil-cf",
		TokenID: "tok",
		Conn:    serverConn,
		Send:    make(chan []byte, 1),
		// CloseFrame: nil (explicit for clarity)
	}
	svc.ClientMu.Lock()
	svc.Clients[client.ID] = client
	svc.ClientMu.Unlock()

	// Must not panic; must return 1.
	if n := svc.RevokeClientByToken("tok"); n != 1 {
		t.Errorf("RevokeClientByToken returned %d, want 1", n)
	}

	// Client read should unblock with an error within a short deadline.
	_ = cc.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := cc.ReadMessage(); err == nil {
		t.Error("expected client read to fail after revoke, got nil")
	}
}

// TestRevokeClientByToken_DuplicateRevokeCoalesces exercises the
// buffer-full path: a second revoke for the same token (while the first
// close frame is still queued on CloseFrame) must not panic, must not
// block, and must fall back to Conn.Close() so the connection still
// terminates. Test uses the pump-backed fixture so CloseFrame is allocated.
func TestRevokeClientByToken_DuplicateRevokeCoalesces(t *testing.T) {
	svc, wsURL, cleanup := wsServiceTestServer(t)
	defer cleanup()

	tokenID := "dup-token"
	url := wsURL + "/?token_id=" + tokenID + "&client_id=dup-a"
	cc, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()

	// Wait for registration.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.ClientMu.RLock()
		_, ok := svc.Clients["dup-a"]
		svc.ClientMu.RUnlock()
		if ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Two back-to-back revokes: both must return 1; neither must panic.
	n1 := svc.RevokeClientByToken(tokenID)
	n2 := svc.RevokeClientByToken(tokenID)
	if n1 != 1 || n2 != 1 {
		t.Errorf("expected both revokes to find 1 client, got %d and %d", n1, n2)
	}

	_ = cc.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := cc.ReadMessage(); err == nil {
		t.Error("expected client read to fail after revoke, got nil")
	}
}
