package gateway

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocketService owns the per-connection state for WebSocket clients:
// the upgrader, client map, active-request cancel map, backpressure
// semaphore, and the gateway-lifecycle context that long-lived read/write
// goroutines use for cancellation.
//
// It was extracted from Gateway (conduit-35t2) to break the god-object.
// Fields are exported because sibling files (gateway.go, ws_chat.go,
// commands.go, shutdown.go) poke the subsystem directly — the lifetime of
// each Client goroutine spans multiple files and methods, and routing every
// access through a narrow accessor would create more churn than the cohesion
// gain is worth.
//
// The service has a lifecycle:
//   - NewWebSocketService constructs with static config (upgrader, cap'd maps).
//   - Start(ctx) binds the gateway-lifecycle context used by client goroutines.
//   - Stop(ctx) closes every remaining client connection and waits (bounded
//     by ctx) for the per-client read/write goroutines to exit. Hijacked
//     WebSocket conns are NOT closed by http.Server.Shutdown (conduit-31jg.25).
type WebSocketService struct {
	logger *slog.Logger

	// Upgrader is the gorilla/websocket upgrader used for /ws.
	Upgrader websocket.Upgrader

	// Clients is the live set of connected clients keyed by Client.ID.
	// Access must be guarded by ClientMu.
	Clients  map[string]*Client
	ClientMu sync.RWMutex

	// WSConnCount is the authoritative counter for MaxWebSocketConnections
	// enforcement. Separate from len(Clients) so the limit can be enforced
	// atomically before taking the write lock.
	WSConnCount atomic.Int32

	// ActiveRequests maps sessionKey → cancel function for the RUNNING turn
	// of that session. Used by /stop and by ShutdownManager's drain phase to
	// cancel stragglers at deadline.
	//
	// conduit-31jg.35: written only by the gateway TurnRunner while it holds
	// the session's turn lock (queued turns are tracked by the runner), for
	// channel, WebSocket, TUI (DirectClient built with Turns), wake and HTTP
	// turns alike.
	ActiveRequests   map[string]context.CancelFunc
	ActiveRequestsMu sync.RWMutex

	// MsgSemaphore throttles concurrent message-processing goroutines spawned
	// by handleClientRead (WebSocket chat) and processMessages (channel
	// ingress). Sized to MaxConcurrentRequests at construction.
	MsgSemaphore chan struct{}

	// Keepalive / deadline tuning (conduit-31jg.25). Zero values fall back
	// to the package defaults; tests shrink them.
	PingInterval time.Duration // how often the pump pings the peer
	PongWait     time.Duration // read deadline, extended by each pong/message
	WriteWait    time.Duration // per-write deadline

	// ctx is the gateway lifecycle context, bound by Start(). Used by
	// handleClientWrite to exit on gateway shutdown and by handleClientRead's
	// reflection deferred-cleanup. nil before Start. Guarded by ctxMu
	// (conduit-1bab).
	ctxMu sync.RWMutex
	ctx   context.Context

	// conns tracks per-client goroutines (read loop + pump) so Stop can wait
	// for them. stopping rejects new Track calls once Stop has begun, which
	// keeps WaitGroup.Add from racing Wait.
	connsMu  sync.Mutex
	conns    sync.WaitGroup
	stopping bool
}

// Default keepalive tuning (conduit-31jg.25). PongWait must exceed
// PingInterval so a healthy peer always answers before the read deadline.
const (
	defaultWSPingInterval = 30 * time.Second
	defaultWSPongWait     = 75 * time.Second
	defaultWSWriteWait    = 10 * time.Second
)

func (s *WebSocketService) pingInterval() time.Duration {
	if s.PingInterval > 0 {
		return s.PingInterval
	}
	return defaultWSPingInterval
}

func (s *WebSocketService) pongWait() time.Duration {
	if s.PongWait > 0 {
		return s.PongWait
	}
	return defaultWSPongWait
}

func (s *WebSocketService) writeWait() time.Duration {
	if s.WriteWait > 0 {
		return s.WriteWait
	}
	return defaultWSWriteWait
}

// NewWebSocketService builds a WebSocketService with the given upgrader,
// backpressure capacity, and logger. Maps are preallocated empty; Start must
// be called before any client goroutines run.
func NewWebSocketService(logger *slog.Logger, upgrader websocket.Upgrader, maxConcurrent int) *WebSocketService {
	if maxConcurrent <= 0 {
		maxConcurrent = MaxConcurrentRequests
	}
	return &WebSocketService{
		logger:         logger,
		Upgrader:       upgrader,
		Clients:        make(map[string]*Client),
		ActiveRequests: make(map[string]context.CancelFunc),
		MsgSemaphore:   make(chan struct{}, maxConcurrent),
	}
}

// Start binds the gateway-lifecycle context used by per-connection goroutines.
// Must be called from Gateway.Start before any WebSocket upgrades are handled.
func (s *WebSocketService) Start(ctx context.Context) {
	s.ctxMu.Lock()
	s.ctx = ctx
	s.ctxMu.Unlock()
}

// Context returns the gateway-lifecycle context bound at Start. Returns a
// non-nil context.Background() fallback if Start has not been called, so
// callers never dereference a nil context.
func (s *WebSocketService) Context() context.Context {
	s.ctxMu.RLock()
	defer s.ctxMu.RUnlock()
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// Track registers n per-client goroutines with the service so Stop can wait
// for them. It returns false once Stop has begun; the caller must then not
// start the goroutines (and should close the conn). A true return must be
// paired with n Untrack calls.
func (s *WebSocketService) Track(n int) bool {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if s.stopping {
		return false
	}
	s.conns.Add(n)
	return true
}

// Untrack marks a goroutine registered via Track as finished.
func (s *WebSocketService) Untrack() { s.conns.Done() }

// Stop drains WebSocket connections (conduit-31jg.25). http.Server.Shutdown
// does not touch hijacked connections, so without this a slow or wedged peer
// kept its read/write goroutines alive past shutdown. Stop rejects new
// connections, asks each pump to send a going-away close frame (the pump
// also does this on gateway-ctx cancel), force-closes the underlying conns
// (Conn.Close is safe concurrently with the pump), and waits for all tracked
// goroutines until ctx expires. Idempotent.
func (s *WebSocketService) Stop(ctx context.Context) {
	s.connsMu.Lock()
	s.stopping = true
	s.connsMu.Unlock()

	s.ClientMu.RLock()
	clients := make([]*Client, 0, len(s.Clients))
	for _, c := range s.Clients {
		clients = append(clients, c)
	}
	s.ClientMu.RUnlock()

	closeMsg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down")
	for _, c := range clients {
		if c.CloseFrame != nil {
			select {
			case c.CloseFrame <- closeMsg:
			default:
			}
		}
	}

	waited := make(chan struct{})
	go func() {
		s.conns.Wait()
		close(waited)
	}()

	// Give pumps a brief window to flush the close frame, then force-close.
	grace := time.NewTimer(500 * time.Millisecond)
	defer grace.Stop()
	select {
	case <-waited:
		return
	case <-grace.C:
	case <-ctx.Done():
	}
	for _, c := range clients {
		if c.Conn != nil {
			_ = c.Conn.Close()
		}
	}
	select {
	case <-waited:
	case <-ctx.Done():
		if s.logger != nil {
			s.logger.Warn("WebSocket drain timed out; some client goroutines still running")
		}
	}
}

// PrepareRead configures the read side of a client connection: message size
// limit, initial read deadline, and a pong handler that extends the deadline.
// A peer that stops answering pings is disconnected after PongWait
// (conduit-31jg.25). Must be called from the read-loop goroutine before the
// first ReadMessage.
func (s *WebSocketService) PrepareRead(client *Client, maxMessageSize int64) {
	if maxMessageSize > 0 {
		client.Conn.SetReadLimit(maxMessageSize)
	}
	s.ExtendReadDeadline(client)
	client.Conn.SetPongHandler(func(string) error {
		s.ExtendReadDeadline(client)
		return nil
	})
}

// ExtendReadDeadline pushes the read deadline PongWait into the future.
// Called on every pong and every inbound message.
func (s *WebSocketService) ExtendReadDeadline(client *Client) {
	_ = client.Conn.SetReadDeadline(time.Now().Add(s.pongWait()))
}

// HandleClientWrite runs the outbound-pump loop for a WebSocket client.
// It monitors the client's Send channel, the CloseFrame signal used by
// off-goroutine callers (e.g. token revocation) to request a specific close
// frame, and the gateway-lifecycle context bound at Start.
//
// Invariant: this goroutine is the ONLY caller of Conn.WriteMessage /
// Conn.WriteControl for the lifetime of the client. Gorilla's
// *websocket.Conn requires that at most one goroutine perform writes via
// the WriteMessage / NextWriter family; routing the revocation close frame
// through CloseFrame enforces that invariant. On exit the deferred
// Conn.Close() tears down the underlying net.Conn so the read goroutine
// also unwinds (conduit-1m5b).
//
// conduit-31jg.25: the pump also exits when the client's read loop ends
// (client.Done()), pings the peer every PingInterval, and bounds every write
// with WriteWait so a half-open peer cannot pin the goroutine.
func (s *WebSocketService) HandleClientWrite(client *Client) {
	defer client.Conn.Close()

	wsCtx := s.Context()
	ticker := time.NewTicker(s.pingInterval())
	defer ticker.Stop()
	done := client.Done()

	for {
		select {
		case message, ok := <-client.Send:
			if !ok {
				// Send channel closed; send WebSocket close frame and exit.
				_ = client.Conn.SetWriteDeadline(time.Now().Add(s.writeWait()))
				_ = client.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			_ = client.Conn.SetWriteDeadline(time.Now().Add(s.writeWait()))
			if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				if s.logger != nil {
					s.logger.Warn("WebSocket write error", "error", err)
				}
				return
			}
		case payload, ok := <-client.CloseFrame:
			// Out-of-band close request (token revoke, admin-initiated
			// disconnect, etc.). Emit the requested close frame and exit;
			// deferred Conn.Close() will tear down the socket.
			if !ok {
				// Channel closed — fall back to empty close frame.
				_ = client.Conn.SetWriteDeadline(time.Now().Add(time.Second))
				_ = client.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			// Short write deadline: we're about to close the conn anyway,
			// a hung peer shouldn't hold the pump goroutine indefinitely.
			_ = client.Conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = client.Conn.WriteMessage(websocket.CloseMessage, payload)
			return
		case <-ticker.C:
			// Keepalive. A peer that stops answering is caught by the read
			// deadline in the read loop; a peer that stops reading is caught
			// here by the write deadline.
			if err := client.Conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(s.writeWait())); err != nil {
				return
			}
		case <-done:
			// Read loop exited (peer gone / read error / deadline). Best-effort
			// close frame; the conn is usually already dead.
			_ = client.Conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = client.Conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		case <-wsCtx.Done():
			// Gateway is shutting down; send close frame and exit.
			_ = client.Conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = client.Conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"))
			return
		}
	}
}

// RevokeClientByToken closes any WebSocket connection authenticated with the
// given token ID. Called by Gateway.handleTokenRevocation, which in turn is
// wired to auth.TokenStorage.OnRevoke at construction.
//
// The revoke hook runs on an arbitrary goroutine, which historically called
// Conn.WriteControl+Close directly and raced with the send-pump's
// WriteMessage calls (gorilla forbids concurrent writers — conduit-1m5b).
// We now hand the close frame to the send-pump via client.CloseFrame so the
// pump is the only goroutine that ever calls Conn.Write*.
//
// If CloseFrame is nil (test fixtures that don't spin up the pump) or full
// (a prior revoke already queued a close), we fall back to Conn.Close(),
// which gorilla documents as safe to call concurrently with writers.
func (s *WebSocketService) RevokeClientByToken(tokenID string) int {
	s.ClientMu.RLock()
	var targets []*Client
	for _, c := range s.Clients {
		if c.TokenID == tokenID {
			targets = append(targets, c)
		}
	}
	s.ClientMu.RUnlock()

	closeMsg := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "token revoked")
	for _, c := range targets {
		if s.logger != nil {
			s.logger.Debug("closing connection for revoked token",
				"client_id", c.ID,
				"token_id", tokenID)
		}

		// Hand off to the send-pump. Non-blocking: if the buffer is full
		// (duplicate revoke) or the channel is nil (legacy test clients),
		// fall through to Conn.Close() which is safe concurrently with the
		// pump's writes per gorilla docs.
		delivered := false
		if c.CloseFrame != nil {
			select {
			case c.CloseFrame <- closeMsg:
				delivered = true
			default:
			}
		}
		if !delivered && c.Conn != nil {
			_ = c.Conn.Close()
		}
	}

	return len(targets)
}
