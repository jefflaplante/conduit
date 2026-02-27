package tui

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"

	"conduit/pkg/protocol"
)

// WSClient manages the WebSocket connection to the gateway
type WSClient struct {
	url       string
	token     string
	userID    string
	conn      *websocket.Conn
	inbox     chan tea.Msg
	connected bool
	mu        sync.RWMutex
	done      chan struct{}
}

// NewWSClient creates a new WebSocket client
func NewWSClient(url, token, userID string) *WSClient {
	return &WSClient{
		url:    url,
		token:  token,
		userID: userID,
		inbox:  make(chan tea.Msg, 256),
		done:   make(chan struct{}),
	}
}

// Connect establishes the WebSocket connection
func (c *WSClient) Connect() error {
	if c.token == "" {
		return fmt.Errorf("no authentication token configured")
	}

	dialer := websocket.Dialer{
		Subprotocols: []string{"conduit-auth", c.token},
	}

	header := http.Header{}
	conn, _, err := dialer.Dial(c.url, header)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.connected = true
	c.mu.Unlock()

	// Start read pump
	go c.readPump()

	return nil
}

// ConnectCmd returns a tea.Cmd that connects to the gateway
func (c *WSClient) ConnectCmd() tea.Cmd {
	return func() tea.Msg {
		if err := c.Connect(); err != nil {
			return DisconnectedMsg{Err: err}
		}
		return ConnectedMsg{}
	}
}

// ListenCmd returns a tea.Cmd that blocks until the next message arrives on the inbox.
// This replaces the old p.Send() pattern and works without a program reference.
func (c *WSClient) ListenCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-c.inbox:
			return msg
		case <-c.done:
			return DisconnectedMsg{}
		}
	}
}

// ReconnectCmd returns a tea.Cmd that reconnects with backoff
func (c *WSClient) ReconnectCmd(attempt int) tea.Cmd {
	return func() tea.Msg {
		// Exponential backoff: 1s, 2s, 4s, 8s, 16s, 30s max
		delay := time.Duration(1<<uint(attempt)) * time.Second
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		time.Sleep(delay)

		if err := c.Connect(); err != nil {
			return DisconnectedMsg{Err: err}
		}
		return ConnectedMsg{}
	}
}

// IsConnected returns the connection status
func (c *WSClient) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Close gracefully disconnects
func (c *WSClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	select {
	case <-c.done:
	default:
		close(c.done)
	}

	if c.conn != nil {
		c.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		c.conn.Close()
		c.conn = nil
	}
	c.connected = false
}

// SendChat sends a chat message to the gateway
func (c *WSClient) SendChat(sessionKey, text string) error {
	requestID := fmt.Sprintf("chat_%d", time.Now().UnixNano())
	return c.SendChatWithID(sessionKey, text, requestID)
}

// SendChatWithID sends a chat message with a specific request ID for correlation
func (c *WSClient) SendChatWithID(sessionKey, text, requestID string) error {
	return c.writeJSON(&protocol.ChatMessage{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeChatMessage,
			ID:        fmt.Sprintf("chat_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: sessionKey,
		UserID:     c.userID,
		RequestID:  requestID,
		Text:       text,
	})
}

// SendCommand sends a slash command to the gateway
func (c *WSClient) SendCommand(sessionKey, command, args string) error {
	return c.writeJSON(&protocol.CommandMessage{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeCommandMessage,
			ID:        fmt.Sprintf("cmd_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: sessionKey,
		Command:    command,
		Args:       args,
	})
}

// CreateSession requests a new session
func (c *WSClient) CreateSession() error {
	requestID := fmt.Sprintf("req_%d", time.Now().UnixNano())
	return c.writeJSON(&protocol.SessionSwitch{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeSessionSwitch,
			ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		UserID:    c.userID,
		Action:    "create",
		RequestID: requestID,
	})
}

// CreateSessionWithID requests a new session with a specific request ID for correlation
func (c *WSClient) CreateSessionWithID(requestID string) error {
	return c.writeJSON(&protocol.SessionSwitch{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeSessionSwitch,
			ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		UserID:    c.userID,
		Action:    "create",
		RequestID: requestID,
	})
}

// SwitchSession switches to a different session
func (c *WSClient) SwitchSession(key string) error {
	return c.writeJSON(&protocol.SessionSwitch{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeSessionSwitch,
			ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: key,
		UserID:     c.userID,
		Action:     "switch",
	})
}

// ListSessions requests the session list
func (c *WSClient) ListSessions() error {
	return c.writeJSON(&protocol.SessionSwitch{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeSessionSwitch,
			ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		UserID: c.userID,
		Action: "list",
	})
}

// writeJSON sends a JSON message through the WebSocket
func (c *WSClient) writeJSON(msg interface{}) error {
	c.mu.RLock()
	conn := c.conn
	connected := c.connected
	c.mu.RUnlock()

	if !connected || conn == nil {
		return fmt.Errorf("not connected")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("connection closed")
	}
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// send enqueues a tea.Msg into the inbox channel (non-blocking, drops if full)
func (c *WSClient) send(msg tea.Msg) {
	select {
	case c.inbox <- msg:
	default:
		log.Printf("WSClient inbox full, dropping message %T", msg)
	}
}

// readPump reads messages from the WebSocket and enqueues them as tea.Msg
func (c *WSClient) readPump() {
	defer func() {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.send(DisconnectedMsg{})
	}()

	for {
		select {
		case <-c.done:
			return
		default:
		}

		c.mu.RLock()
		conn := c.conn
		c.mu.RUnlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
				log.Printf("WebSocket read error: %v", err)
			}
			return
		}

		parsed, err := protocol.ParseMessage(message)
		if err != nil {
			log.Printf("Failed to parse message: %v", err)
			continue
		}

		switch msg := parsed.(type) {
		case *protocol.StreamStart:
			c.send(StreamStartMsg{
				SessionKey: msg.SessionKey,
				RequestID:  msg.RequestID,
			})

		case *protocol.StreamDelta:
			c.send(StreamDeltaMsg{
				SessionKey: msg.SessionKey,
				RequestID:  msg.RequestID,
				Delta:      msg.Delta,
			})

		case *protocol.StreamEnd:
			c.send(StreamEndMsg{
				SessionKey:       msg.SessionKey,
				RequestID:        msg.RequestID,
				Content:          msg.Content,
				PromptTokens:     msg.PromptTokens,
				CompletionTokens: msg.CompletionTokens,
				TotalTokens:      msg.TotalTokens,
				Model:            msg.Model,
				RequestCost:      msg.RequestCost,
				SessionCost:      msg.SessionCost,
			})

		case *protocol.ToolEvent:
			c.send(ToolEventMsg{ToolEvent: *msg})

		case *protocol.CommandResponse:
			c.send(CommandResponseMsg{
				SessionKey: msg.SessionKey,
				Command:    msg.Command,
				Response:   msg.Response,
				Model:      msg.Model,
			})

		case *protocol.SessionSwitch:
			switch msg.Action {
			case "created":
				c.send(SessionCreatedMsg{
					Key:       msg.SessionKey,
					RequestID: msg.RequestID,
					CreatedAt: msg.CreatedAt,
				})
			case "switched":
				c.send(SessionSwitchedMsg{
					Key:       msg.SessionKey,
					History:   msg.History,
					Model:     msg.Model,
					CreatedAt: msg.CreatedAt,
				})
			case "list":
				c.send(SessionListMsg{Sessions: msg.Sessions})
			}

		case *protocol.ErrorResponse:
			c.send(ErrorMsg{
				SessionKey: msg.SessionKey,
				Code:       msg.Code,
				Message:    msg.Message,
			})

		case *protocol.GatewayInfo:
			c.send(GatewayInfoMsg{
				AssistantName: msg.AssistantName,
				Version:       msg.Version,
				GitCommit:     msg.GitCommit,
				UptimeSeconds: msg.UptimeSeconds,
				ModelAliases:  msg.ModelAliases,
				ToolCount:     msg.ToolCount,
				SkillCount:    msg.SkillCount,
			})

		case *protocol.HealthCheck:
			// Ignore health check responses
		}
	}
}
