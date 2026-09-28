package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"conduit/internal/protocol"
	"conduit/internal/version"

	"github.com/gorilla/websocket"
)

// Client represents a WebSocket client connection
type Client struct {
	ID      string
	Role    string // "client" or "node"
	UserID  string // user identity for session scoping
	TokenID string // auth token ID used for this connection (for revocation)
	Conn    *websocket.Conn
	Send    chan []byte

	// CloseFrame carries an out-of-band signal from off-goroutine callers
	// (e.g. RevokeClientByToken running on the auth-revoke hook) asking the
	// send-pump to emit a WebSocket close frame with a specific payload
	// before exiting. The send-pump is the only goroutine that calls
	// Conn.Write*, so routing close frames through here guarantees
	// serialization and avoids the race where a revoker would call
	// WriteControl/Close concurrently with an in-flight WriteMessage on
	// the pump. Buffered size 1: duplicate revokes coalesce harmlessly
	// (first non-blocking send wins, the rest drop). nil on pre-existing
	// test fixtures is tolerated.
	CloseFrame chan []byte

	// conduit-31jg.25: the active session key is written by chat /
	// session-switch goroutines and read by the read-loop teardown and the
	// shutdown breadcrumb, so it lives behind mu. Use SessionKey() /
	// SetSessionKey() (ws_client.go). done is closed when the read loop
	// exits so the send-pump stops instead of leaking until shutdown.
	mu         sync.Mutex
	sessionKey string
	done       chan struct{}
	doneOnce   sync.Once
}

// conduit-31jg.25: synchronized accessors and lifecycle signal for Client.

// SessionKey returns the client's active session key. Safe for concurrent use.
func (c *Client) SessionKey() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionKey
}

// SetSessionKey updates the client's active session key. Safe for concurrent use.
func (c *Client) SetSessionKey(key string) {
	c.mu.Lock()
	c.sessionKey = key
	c.mu.Unlock()
}

// Done returns a channel that is closed once the client's read loop has
// exited (peer gone, read error, deadline). The send-pump selects on it so
// it does not outlive the connection.
func (c *Client) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done == nil {
		c.done = make(chan struct{})
	}
	return c.done
}

// markDone closes Done(). Idempotent.
func (c *Client) markDone() {
	c.doneOnce.Do(func() {
		c.mu.Lock()
		if c.done == nil {
			c.done = make(chan struct{})
		}
		close(c.done)
		c.mu.Unlock()
	})
}

// handleWebSocket handles WebSocket connections with authentication.
// This stays on *Gateway because it is the HTTP handler entry point and
// needs orchestrator-level access to auth (wsAuthenticator), metrics, tools,
// skills, and the channel manager for the initial GatewayInfo frame. It
// hands off to WebSocketService for connection-state bookkeeping.
func (g *Gateway) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check WebSocket connection limit before doing any work
	if g.ws.WSConnCount.Load() >= MaxWebSocketConnections {
		http.Error(w, "Too many WebSocket connections", http.StatusServiceUnavailable)
		g.logger.Warn("WebSocket connection rejected: limit reached",
			"current", g.ws.WSConnCount.Load(),
			"max", MaxWebSocketConnections)
		return
	}

	// Authenticate the WebSocket upgrade request
	authResult := g.auth.WSAuthenticator.Authenticate(r)
	if !authResult.Authenticated {
		g.auth.WSAuthenticator.RejectUpgrade(w, authResult.Error)
		return
	}

	// Build response header for protocol negotiation
	var responseHeader http.Header
	if authResult.ResponseProtocol != "" {
		responseHeader = http.Header{
			"Sec-WebSocket-Protocol": []string{authResult.ResponseProtocol},
		}
	}

	// Atomically increment and check the connection count.
	// Re-check after increment to handle races between the Load() above and now.
	if count := g.ws.WSConnCount.Add(1); count > MaxWebSocketConnections {
		g.ws.WSConnCount.Add(-1)
		http.Error(w, "Too many WebSocket connections", http.StatusServiceUnavailable)
		g.logger.Warn("WebSocket connection rejected (race): limit reached",
			"current", count-1,
			"max", MaxWebSocketConnections)
		return
	}

	conn, err := g.ws.Upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		g.ws.WSConnCount.Add(-1) // Decrement on upgrade failure
		g.logger.Error("WebSocket upgrade error", "error", err)
		return
	}

	client := &Client{
		ID:         fmt.Sprintf("client_%d", time.Now().UnixNano()),
		Role:       authResult.AuthInfo.ClientName, // Store authenticated client name
		UserID:     authResult.AuthInfo.ClientName, // Default user identity from auth
		TokenID:    authResult.AuthInfo.TokenID,    // Track token for revocation
		Conn:       conn,
		Send:       make(chan []byte, 256),
		CloseFrame: make(chan []byte, 1),
	}

	g.ws.ClientMu.Lock()
	g.ws.Clients[client.ID] = client
	clientCount := len(g.ws.Clients)
	g.ws.ClientMu.Unlock()

	// Update metrics
	if g.monitoring.MetricsCollector != nil {
		g.monitoring.MetricsCollector.UpdateWebSocketConnections(clientCount)
	}

	g.logger.Info("client connected", "client_id", client.ID, "auth", authResult.AuthInfo.ClientName)

	// Send enriched gateway info to client
	toolCount := len(g.tools.GetAvailableTools())
	var skillCount int
	if g.skillsManager != nil {
		if skills, err := g.skillsManager.GetAvailableSkills(context.Background()); err == nil {
			skillCount = len(skills)
		}
	}
	g.sendToClient(client, &protocol.GatewayInfo{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeGatewayInfo,
			ID:        fmt.Sprintf("gi_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		AssistantName: g.config.Agent.Name,
		Version:       version.Info(),
		GitCommit:     version.GitCommit,
		UptimeSeconds: int64(g.monitoring.GatewayMetrics.GetUptime().Seconds()),
		ModelAliases:  g.getModelAliases(),
		ToolCount:     toolCount,
		SkillCount:    skillCount,
	})

	// Handle client in separate goroutines.
	// Use g.lifecycleCtx() (gateway lifecycle) instead of r.Context() because the HTTP request
	// context is cancelled when this handler returns, which happens immediately
	// after spawning these goroutines.
	//
	// conduit-31jg.25: both goroutines are tracked so WebSocketService.Stop
	// can wait for them; if Stop already began, tear the conn down instead.
	if !g.ws.Track(2) {
		g.ws.ClientMu.Lock()
		delete(g.ws.Clients, client.ID)
		g.ws.ClientMu.Unlock()
		g.ws.WSConnCount.Add(-1)
		_ = conn.Close()
		return
	}
	go func() {
		defer g.ws.Untrack()
		g.handleClientWrite(client)
	}()
	go func() {
		defer g.ws.Untrack()
		g.handleClientRead(g.lifecycleCtx(), client)
	}()
}

// handleTokenRevocation closes all WebSocket connections authenticated with the
// given token. This is called by TokenStorage.OnRevoke as a best-effort
// operation -- errors from already-closing connections are silently ignored.
//
// The name stays on *Gateway (per conduit-23rz) to keep the symbol stable for
// existing callers and tests. It delegates to WebSocketService.
func (g *Gateway) handleTokenRevocation(tokenID string) {
	if g.ws == nil {
		return
	}
	n := g.ws.RevokeClientByToken(tokenID)
	if n > 0 {
		g.logger.Info("closed connections for revoked token",
			"connection_count", n,
			"token_id", tokenID)
	}
}

// handleClientRead handles incoming messages from a WebSocket client
func (g *Gateway) handleClientRead(ctx context.Context, client *Client) {
	defer func() {
		// conduit-31jg.25: stop the send-pump now rather than at gateway shutdown.
		client.markDone()

		// SPAR reflection: fire low-confidence (Go-only) reflection on WS disconnect
		// for substantive sessions. This runs before cleanup so the session data is
		// still available.
		if sk := client.SessionKey(); sk != "" {
			reflCtx, reflCancel := context.WithTimeout(g.lifecycleCtx(), 5*time.Second)
			g.reflectOnSessionEnd(reflCtx, sk)
			reflCancel()
		}

		g.ws.ClientMu.Lock()
		delete(g.ws.Clients, client.ID)
		clientCount := len(g.ws.Clients)
		g.ws.ClientMu.Unlock()

		// Decrement active WebSocket connection count
		g.ws.WSConnCount.Add(-1)

		// Update metrics
		if g.monitoring.MetricsCollector != nil {
			g.monitoring.MetricsCollector.UpdateWebSocketConnections(clientCount)
		}

		client.Conn.Close()
		g.logger.Debug("client disconnected", "client_id", client.ID)
	}()

	// Set message size limit to prevent DoS via large messages, plus read
	// deadline + pong handler so half-open peers are reaped (conduit-31jg.25).
	g.ws.PrepareRead(client, g.config.WebSocket.GetMaxMessageSize())

	for {
		_, message, err := client.Conn.ReadMessage()
		if err == nil {
			g.ws.ExtendReadDeadline(client)
		}
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				g.logger.Debug("client closed connection normally", "client_id", client.ID)
			} else {
				g.logger.Warn("WebSocket read error", "client_id", client.ID, "error", err)
			}
			break
		}

		parsed, err := protocol.ParseMessage(message)
		if err != nil {
			g.logger.Warn("failed to parse message", "client_id", client.ID, "error", err)
			continue
		}

		switch msg := parsed.(type) {
		case *protocol.ChatMessage:
			if g.shutdownMgr != nil && g.shutdownMgr.IsDraining() {
				g.sendToClient(client, map[string]string{
					"type":    "system",
					"content": "Gateway is restarting — not accepting new requests.",
				})
				continue
			}
			select {
			case g.ws.MsgSemaphore <- struct{}{}:
				go func() {
					defer func() { <-g.ws.MsgSemaphore }()
					g.handleWebSocketChat(ctx, client, msg)
				}()
			default:
				g.recordWebSocketDrop(client, msg, "msg_semaphore_full")
			}
		case *protocol.CommandMessage:
			go g.handleWebSocketCommand(ctx, client, msg)
		case *protocol.SessionSwitch:
			go g.handleWebSocketSessionSwitch(client, msg)
		case *protocol.HealthCheck:
			g.sendToClient(client, &protocol.HealthCheck{
				BaseMessage: protocol.BaseMessage{
					Type:      protocol.TypeHealthCheck,
					ID:        fmt.Sprintf("health_%d", time.Now().UnixNano()),
					Timestamp: time.Now(),
				},
				Status: "ok",
			})
		default:
			g.logger.Debug("unhandled message type", "client_id", client.ID, "type", fmt.Sprintf("%T", msg))
		}
	}
}

// handleClientWrite is a thin wrapper that delegates to WebSocketService.
// Kept on *Gateway only so the goroutine-spawn call site in handleWebSocket
// reads naturally; the actual loop lives on WebSocketService.
func (g *Gateway) handleClientWrite(client *Client) {
	g.ws.HandleClientWrite(client)
}
