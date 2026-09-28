package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"conduit/internal/approval"
	"conduit/internal/protocol"
	"conduit/internal/tui"
)

// Human-in-the-loop approvals (conduit-31jg.43).
//
// Interactive entry points (channel adapters via handleIncomingMessage,
// WebSocket chat, in-process TUI) attach an interactive approval.Origin to
// the turn context and offer every inbound human message to
// g.approvals.HandleReply BEFORE it is stored, busy-acked or handed to the
// AI router. The approval therefore never waits behind the per-session turn
// lock, and only a human inbound message can grant it. Every other entry
// point (heartbeat, cron, sub-agents, session wake, HTTP test API) carries
// no interactive origin and fails closed.

// initApprovals creates the approval manager and wires it into the skill
// executor. Safe to call once during construction.
func (g *Gateway) initApprovals() {
	g.approvals = approval.NewManager(approval.Config{
		Logger:     g.logger,
		OnResolved: g.recordApprovalOutcome,
	})
	if g.skillsManager != nil {
		g.skillsManager.SetApprover(g.approvals)
	}
}

// recordApprovalOutcome notes the decision in the session transcript so the
// model learns what happened on its next turn. Body text is never included.
func (g *Gateway) recordApprovalOutcome(r approval.Resolution) {
	g.recordConfigApproval(r) // conduit-2qes
	if g.sessions == nil || r.Ticket.SessionKey == "" {
		return
	}
	var note string
	switch {
	case r.Decision == approval.DecisionApproved && r.Err == nil:
		note = fmt.Sprintf("[System note: the owner APPROVED request %s (%s); it has been carried out.]", r.Ticket.Code, r.Action.Title)
	case r.Decision == approval.DecisionApproved:
		note = fmt.Sprintf("[System note: the owner approved request %s (%s), but it FAILED: %v]", r.Ticket.Code, r.Action.Title, r.Err)
	case r.Decision == approval.DecisionDenied:
		note = fmt.Sprintf("[System note: the owner DENIED request %s (%s); nothing was done.]", r.Ticket.Code, r.Action.Title)
	default:
		note = fmt.Sprintf("[System note: request %s (%s) EXPIRED without approval; nothing was done.]", r.Ticket.Code, r.Action.Title)
	}
	if _, err := g.sessions.AddMessage(r.Ticket.SessionKey, "assistant", note, nil); err != nil {
		g.logger.Warn("approval: failed to record outcome in session", "session_key", r.Ticket.SessionKey, "error", err)
	}
}

// channelApprovalNotifier reaches the human on a channel adapter
// (Telegram, TUI adapter). Unknown channel => error => fail closed.
func (g *Gateway) channelApprovalNotifier(channelID, userID, sessionKey string) approval.Notifier {
	return func(ctx context.Context, n approval.Notice) error {
		if g.channelManager == nil {
			return errors.New("no channel manager")
		}
		if _, ok := g.channelManager.GetAdapter(channelID); !ok {
			return fmt.Errorf("channel %q not available for approval prompts", channelID)
		}
		md := map[string]string{approval.MetaNotice: "1"}
		if len(n.Choices) > 0 {
			if b, err := json.Marshal(n.Choices); err == nil {
				md[approval.MetaChoices] = string(b)
			}
		}
		return g.channelManager.SendMessage(&protocol.OutgoingMessage{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeOutgoingMessage,
				ID:        fmt.Sprintf("approval_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			ChannelID:  channelID,
			SessionKey: sessionKey,
			UserID:     userID,
			Text:       n.Text,
			Metadata:   md,
		})
	}
}

// wsApprovalNotifier reaches a WebSocket chat client.
func (g *Gateway) wsApprovalNotifier(client *Client, sessionKey string) approval.Notifier {
	return func(ctx context.Context, n approval.Notice) error {
		if client == nil {
			return errors.New("no websocket client")
		}
		g.sendToClient(client, &protocol.CommandResponse{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeCommandResponse,
				ID:        fmt.Sprintf("approval_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: sessionKey,
			Command:    "approval",
			Response:   n.Text,
		})
		return nil
	}
}

// directApprovalNotifier reaches the in-process TUI (SSH) client.
func (c *DirectClient) directApprovalNotifier(sessionKey string) approval.Notifier {
	return func(ctx context.Context, n approval.Notice) error {
		if c.ctx.Err() != nil {
			return errors.New("tui client disconnected")
		}
		c.send(tui.CommandResponseMsg{SessionKey: sessionKey, Command: "approval", Response: n.Text})
		return nil
	}
}
