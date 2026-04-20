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
//   - Stop() is a no-op today; shutdown is orchestrated by ShutdownManager
//     (drain active requests) plus the gateway's HTTP server shutdown (closes
//     connections), which causes the per-client read/write goroutines to exit.
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

	// ActiveRequests maps sessionKey → cancel function for the in-flight AI
	// request associated with that session. Used by /stop and by
	// ShutdownManager's drain phase to cancel stragglers at deadline.
	//
	// NOTE: direct_client.go (TUI path) keeps its OWN separate activeRequests
	// map on the DirectClient struct — the maps are intentionally not shared.
	// The TUI bypasses WebSocket entirely and is a single-process-one-user
	// path; merging the two would couple unrelated lifecycles.
	ActiveRequests   map[string]context.CancelFunc
	ActiveRequestsMu sync.RWMutex

	// MsgSemaphore throttles concurrent message-processing goroutines spawned
	// by handleClientRead (WebSocket chat) and processMessages (channel
	// ingress). Sized to MaxConcurrentRequests at construction.
	MsgSemaphore chan struct{}

	// ctx is the gateway lifecycle context, bound by Start(). Used by
	// handleClientWrite to exit on gateway shutdown and by handleClientRead's
	// reflection deferred-cleanup. nil before Start.
	ctx context.Context
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
	s.ctx = ctx
}

// Context returns the gateway-lifecycle context bound at Start. Returns a
// non-nil context.Background() fallback if Start has not been called, so
// callers never dereference a nil context.
func (s *WebSocketService) Context() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// Stop is a no-op today. WebSocket connections are torn down by HTTP server
// shutdown (which closes the underlying net.Conn and drives per-client read
// goroutines to error out) and by the gateway-lifecycle context being
// cancelled (which unblocks handleClientWrite). Active requests are drained
// by ShutdownManager before the context is cancelled.
//
// Exposed as a method so the lifecycle surface is explicit and future
// graceful-close logic has a home.
func (s *WebSocketService) Stop() {
	// Intentionally empty. See doc comment.
}

// HandleClientWrite runs the outbound-pump loop for a WebSocket client.
// It monitors both the client's Send channel and the gateway-lifecycle
// context bound at Start. When the gateway shuts down, it sends a WebSocket
// close message and exits, preventing goroutine leaks from long-lived
// connections.
func (s *WebSocketService) HandleClientWrite(client *Client) {
	defer client.Conn.Close()

	wsCtx := s.Context()
	for {
		select {
		case message, ok := <-client.Send:
			if !ok {
				// Send channel closed; send WebSocket close frame and exit.
				client.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				if s.logger != nil {
					s.logger.Warn("WebSocket write error", "error", err)
				}
				return
			}
		case <-wsCtx.Done():
			// Gateway is shutting down; send close frame and exit.
			client.Conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"))
			return
		}
	}
}

// RevokeClientByToken closes any WebSocket connection authenticated with the
// given token ID. Called by Gateway.handleTokenRevocation, which in turn is
// wired to auth.TokenStorage.OnRevoke at construction. Best-effort: errors
// from already-closed connections are swallowed.
func (s *WebSocketService) RevokeClientByToken(tokenID string) int {
	s.ClientMu.RLock()
	var targets []*Client
	for _, c := range s.Clients {
		if c.TokenID == tokenID {
			targets = append(targets, c)
		}
	}
	s.ClientMu.RUnlock()

	for _, c := range targets {
		if s.logger != nil {
			s.logger.Debug("closing connection for revoked token",
				"client_id", c.ID,
				"token_id", tokenID)
		}
		closeMsg := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "token revoked")
		_ = c.Conn.WriteControl(websocket.CloseMessage, closeMsg, time.Now().Add(time.Second))
		_ = c.Conn.Close()
	}

	return len(targets)
}
