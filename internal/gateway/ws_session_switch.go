package gateway

import (
	"fmt"
	"strings"
	"time"

	"conduit/internal/protocol"
)

// handleWebSocketSessionSwitch handles session management requests
func (g *Gateway) handleWebSocketSessionSwitch(client *Client, msg *protocol.SessionSwitch) {
	userID := msg.UserID
	if userID == "" {
		userID = client.UserID
	}
	if userID == "" {
		userID = client.Role
	}

	switch msg.Action {
	case "create":
		// Create a new session
		channelID := fmt.Sprintf("tui_%s_%d", userID, time.Now().UnixNano())
		session, err := g.sessions.GetOrCreateSession(userID, channelID)
		if err != nil {
			g.sendErrorToClient(client, "", "session_error", fmt.Sprintf("Failed to create session: %v", err))
			return
		}
		client.SetSessionKey(session.Key) // conduit-31jg.25

		g.sendToClient(client, &protocol.SessionSwitch{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeSessionSwitch,
				ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: session.Key,
			Action:     "created",
			RequestID:  msg.RequestID, // Pass through the request ID for correlation
			CreatedAt:  session.CreatedAt,
		})

	case "switch":
		if msg.SessionKey == "" {
			g.sendErrorToClient(client, "", "invalid_request", "Session key required for switch")
			return
		}

		// Verify session exists
		session, err := g.sessions.GetSession(msg.SessionKey)
		if err != nil {
			g.sendErrorToClient(client, "", "session_error", fmt.Sprintf("Session not found: %v", err))
			return
		}

		client.SetSessionKey(session.Key) // conduit-31jg.25

		// Get message history for the session
		messages, _ := g.sessions.GetMessages(session.Key, 100)
		var history []protocol.MessageInfo
		for _, m := range messages {
			history = append(history, protocol.MessageInfo{
				Role:      m.Role,
				Content:   m.Content,
				Timestamp: m.Timestamp,
			})
		}

		g.sendToClient(client, &protocol.SessionSwitch{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeSessionSwitch,
				ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: session.Key,
			Action:     "switched",
			Model:      session.Context["model"],
			CreatedAt:  session.CreatedAt,
			History:    history,
		})

	case "list":
		// Get all sessions for this user
		sessions, err := g.sessions.GetSessionsByUser(userID, 50)
		if err != nil {
			g.sendErrorToClient(client, "", "session_error", fmt.Sprintf("Failed to list sessions: %v", err))
			return
		}

		var sessionInfos []protocol.SessionInfo
		for _, s := range sessions {
			// Determine origin tag from channel ID
			origin := "TUI"
			if strings.HasPrefix(s.ChannelID, "telegram") {
				origin = "TG"
			} else if strings.HasPrefix(s.ChannelID, "ssh") {
				origin = "SSH"
			}

			sessionInfos = append(sessionInfos, protocol.SessionInfo{
				Key:          s.Key,
				UserID:       s.UserID,
				ChannelID:    s.ChannelID,
				CreatedAt:    s.CreatedAt,
				LastMessage:  s.UpdatedAt,
				MessageCount: s.MessageCount,
				Metadata:     map[string]string{"origin": origin},
			})
		}

		g.sendToClient(client, &protocol.SessionSwitch{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeSessionSwitch,
				ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			Action:   "list",
			Sessions: sessionInfos,
		})

	default:
		g.sendErrorToClient(client, "", "invalid_request", fmt.Sprintf("Unknown session action: %s", msg.Action))
	}
}
