package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/config"
)

// conduit-31jg.25 regression tests: per-connection goroutine lifecycle,
// keepalive/deadlines, SessionKey synchronization, and Stop draining.

// newWSLifecycleGateway builds the minimum Gateway needed to run the real
// handleClientRead / handleClientWrite pair behind an httptest server. The
// handler mirrors the post-upgrade half of Gateway.handleWebSocket.
func newWSLifecycleGateway(t *testing.T, tune func(*WebSocketService)) (*Gateway, string, func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ws := NewWebSocketService(logger, websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}, 16)
	if tune != nil {
		tune(ws)
	}
	ctx, cancel := context.WithCancel(context.Background())
	gw := &Gateway{
		logger:     logger,
		ws:         ws,
		config:     &config.Config{},
		monitoring: &MonitoringService{},
		ctx:        ctx,
	}
	ws.Start(ctx)

	var n int
	var nMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		nMu.Lock()
		n++
		id := fmt.Sprintf("c%d", n)
		nMu.Unlock()
		client := &Client{ID: id, Conn: conn, Send: make(chan []byte, 256), CloseFrame: make(chan []byte, 1)}
		ws.ClientMu.Lock()
		ws.Clients[id] = client
		ws.ClientMu.Unlock()
		ws.WSConnCount.Add(1)
		if !ws.Track(2) {
			_ = conn.Close()
			return
		}
		go func() { defer ws.Untrack(); gw.handleClientWrite(client) }()
		go func() { defer ws.Untrack(); gw.handleClientRead(ctx, client) }()
	}))
	cleanup := func() {
		cancel()
		srv.Close()
	}
	return gw, "ws" + strings.TrimPrefix(srv.URL, "http"), cleanup
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func clientCount(ws *WebSocketService) int {
	ws.ClientMu.RLock()
	defer ws.ClientMu.RUnlock()
	return len(ws.Clients)
}

// Before the fix the send-pump only exited on gateway shutdown, so every
// connect/disconnect left one goroutine (plus its 256-slot buffer) behind.
func TestWebSocket_PumpExitsOnDisconnect_NoGoroutineLeak(t *testing.T) {
	gw, url, cleanup := newWSLifecycleGateway(t, nil)
	defer cleanup()

	cycle := func() {
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		waitFor(t, 2*time.Second, "client registered", func() bool { return clientCount(gw.ws) == 1 })
		_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = c.Close()
		waitFor(t, 2*time.Second, "client removed", func() bool { return clientCount(gw.ws) == 0 })
	}

	cycle() // warm up (httptest, dialer internals)
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const n = 25
	for i := 0; i < n; i++ {
		cycle()
	}

	waitFor(t, 3*time.Second, "goroutines back to baseline", func() bool {
		return runtime.NumGoroutine() <= baseline+2
	})

	// All tracked goroutines must be done: Stop should return immediately.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	start := time.Now()
	gw.ws.Stop(stopCtx)
	if time.Since(start) > time.Second {
		t.Fatalf("Stop waited %v; per-client goroutines leaked", time.Since(start))
	}
}

// A peer that connects and then never reads (so never answers pings) must be
// reaped by the read deadline instead of holding a slot forever.
func TestWebSocket_HalfOpenPeerReapedByPingDeadline(t *testing.T) {
	gw, url, cleanup := newWSLifecycleGateway(t, func(ws *WebSocketService) {
		ws.PingInterval = 50 * time.Millisecond
		ws.PongWait = 250 * time.Millisecond
		ws.WriteWait = 250 * time.Millisecond
	})
	defer cleanup()

	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// Never call c.ReadMessage: gorilla only answers pings from inside a read,
	// so this peer looks half-open to the server.
	waitFor(t, 2*time.Second, "client registered", func() bool { return clientCount(gw.ws) == 1 })
	waitFor(t, 3*time.Second, "half-open client reaped", func() bool { return clientCount(gw.ws) == 0 })
	if got := gw.ws.WSConnCount.Load(); got != 0 {
		t.Fatalf("WSConnCount = %d, want 0", got)
	}
}

// A responsive peer (reads, so answers pings) must survive several ping
// intervals past PongWait.
func TestWebSocket_ResponsivePeerKeptAliveByPongs(t *testing.T) {
	gw, url, cleanup := newWSLifecycleGateway(t, func(ws *WebSocketService) {
		ws.PingInterval = 40 * time.Millisecond
		ws.PongWait = 150 * time.Millisecond
	})
	defer cleanup()

	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
	waitFor(t, 2*time.Second, "client registered", func() bool { return clientCount(gw.ws) == 1 })
	time.Sleep(600 * time.Millisecond) // 4x PongWait
	if clientCount(gw.ws) != 1 {
		t.Fatal("responsive client was disconnected")
	}
}

// Stop must close hijacked WebSocket conns (http.Server.Shutdown does not),
// send a going-away frame, wait for goroutines, and reject late connections.
func TestWebSocketService_StopDrainsConnections(t *testing.T) {
	gw, url, cleanup := newWSLifecycleGateway(t, nil)
	defer cleanup()

	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	waitFor(t, 2*time.Second, "client registered", func() bool { return clientCount(gw.ws) == 1 })

	closeCode := make(chan int, 1)
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				if ce, ok := err.(*websocket.CloseError); ok {
					closeCode <- ce.Code
				} else {
					closeCode <- -1
				}
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	gw.ws.Stop(ctx)
	if ctx.Err() != nil {
		t.Fatal("Stop hit its deadline; goroutines did not exit")
	}
	select {
	case code := <-closeCode:
		if code != websocket.CloseGoingAway {
			t.Errorf("close code = %d, want %d", code, websocket.CloseGoingAway)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer never observed close")
	}
	if gw.ws.Track(1) {
		t.Fatal("Track succeeded after Stop")
	}
	gw.ws.Stop(ctx) // idempotent
}

// -race: SessionKey is written by chat/session-switch goroutines and read by
// the shutdown breadcrumb and read-loop teardown.
func TestClient_SessionKeyConcurrentAccess(t *testing.T) {
	gw := newTestGatewayForShutdown(t, &config.Config{DataDir: t.TempDir()})
	c := &Client{ID: "c1"}
	gw.ws.Clients[c.ID] = c
	sm := NewShutdownManager(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), gw)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.SetSessionKey(fmt.Sprintf("s-%d-%d", i, j))
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			sm.writeBreadcrumb(nil)
			_ = c.SessionKey()
		}
	}()
	wg.Wait()
}

// conduit-1bab: Start and Context may run concurrently without a data race.
func TestWebSocketService_ConcurrentStartAndContext(t *testing.T) {
	ws := NewWebSocketService(nil, websocket.Upgrader{}, 1)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); ws.Start(context.Background()) }()
		go func() { defer wg.Done(); _ = ws.Context() }()
	}
	wg.Wait()
	if ws.Context() == nil {
		t.Fatal("nil context")
	}
}
