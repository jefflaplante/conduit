package gateway

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/channels"
	"conduit/internal/logging"
	"conduit/internal/protocol"
)

// startChannels initializes and starts the channel manager
func (g *Gateway) startChannels(ctx context.Context) error {
	// Convert config channels to channel configs
	var channelConfigs []channels.ChannelConfig

	for _, chConfig := range g.config.Channels {
		channelConfig := channels.ChannelConfig{
			ID:      chConfig.Name,
			Type:    chConfig.Type,
			Name:    chConfig.Name,
			Enabled: chConfig.Enabled,
			Config:  chConfig.Config,
		}
		channelConfigs = append(channelConfigs, channelConfig)
	}

	// Start channel manager
	if err := g.channelManager.Start(ctx, channelConfigs); err != nil {
		return fmt.Errorf("failed to start channel manager: %w", err)
	}

	g.logger.Info("channel manager started")
	return nil
}

// stopChannels stops the channel manager
func (g *Gateway) stopChannels() {
	if err := g.channelManager.Stop(); err != nil {
		g.logger.Error("error stopping channel manager", "error", err)
	}
}

// processMessages handles the main message processing loop
func (g *Gateway) processMessages(ctx context.Context) {
	g.logger.Debug("starting message processor")

	for {
		select {
		case msg := <-g.channelManager.ReceiveMessages():
			if g.shutdownMgr != nil && g.shutdownMgr.IsDraining() {
				g.logger.Warn("rejecting channel message during shutdown drain",
					"channel_id", msg.ChannelID)
				continue
			}
			select {
			case g.ws.MsgSemaphore <- struct{}{}:
				go func() {
					defer func() { <-g.ws.MsgSemaphore }()
					g.handleIncomingMessage(ctx, msg)
				}()
			default:
				g.recordIngestDrop(msg, "msg_semaphore_full")
			}

		case <-ctx.Done():
			return
		}
	}
}

// handleIncomingMessage processes a single incoming message
func (g *Gateway) handleIncomingMessage(ctx context.Context, msg *protocol.IncomingMessage) {
	// Add request ID to context for correlation
	ctx = logging.WithRequestID(ctx, "")
	reqID := logging.RequestIDFromContext(ctx)

	g.logger.Debug("processing message",
		"channel_id", msg.ChannelID,
		"text_length", len(msg.Text),
		"request_id", reqID)

	// Track activity in metrics collector
	if g.monitoring != nil && g.monitoring.MetricsCollector != nil {
		g.monitoring.MetricsCollector.MarkActivity()
	}

	// Get or create session
	session, err := g.sessions.GetOrCreateSession(msg.UserID, msg.ChannelID)
	if err != nil {
		logging.Error(ctx, "error getting session", "error", err)
		return
	}

	// conduit-31jg.43: approval replies are consumed here, before the
	// busy-ack, the transcript and the per-session turn lock, so a pending
	// approval never waits behind (or deadlocks with) an in-flight turn.
	notify := g.channelApprovalNotifier(msg.ChannelID, msg.UserID, msg.SessionKey)
	if g.approvals.HandleReply(ctx, approval.Inbound{
		ChannelID: msg.ChannelID, UserID: msg.UserID, SessionKey: session.Key,
		Text: msg.Text, Notify: notify,
	}) {
		return
	}

	// Handle commands before AI processing
	if handled := g.handleCommand(ctx, msg, session); handled {
		return
	}

	// Reset wake_depth on normal user messages so the recursion guard resets
	// after a human sends a message to the session.
	if session.Context["wake_depth"] != "" && session.Context["wake_depth"] != "0" {
		_ = g.sessions.SetSessionContext(session.Key, "wake_depth", "0")
	}

	if g.ai == nil {
		// Echo back if no AI available (for testing)
		echoMsg := &protocol.OutgoingMessage{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeOutgoingMessage,
				ID:        fmt.Sprintf("echo_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			ChannelID:  msg.ChannelID,
			SessionKey: msg.SessionKey,
			UserID:     msg.UserID,
			Text:       fmt.Sprintf("Echo: %s", msg.Text),
		}
		g.channelManager.SendMessage(echoMsg)
		return
	}

	// Transcript form of the message: a text marker for photos, not binary data.
	textToStore := msg.Text
	var aiAttachments []ai.Attachment
	if len(msg.Attachments) > 0 {
		if textToStore == "" {
			textToStore = "[Sent a photo]"
		} else {
			textToStore = "[Photo] " + textToStore
		}
		// Thread image attachments to the AI layer for vision analysis
		aiAttachments = make([]ai.Attachment, len(msg.Attachments))
		for i, att := range msg.Attachments {
			aiAttachments[i] = ai.Attachment{Type: att.Type, MediaType: att.MediaType, Data: att.Data}
		}
	}

	// conduit-31jg.35: the shared TurnRunner owns the busy-ack decision, the
	// turn lock, transcript persistence (inside the lock, conduit-31jg.22),
	// /stop registration (conduit-31jg.23), reflection and compaction
	// (conduit-31jg.49). This adapter only renders output for the channel.
	g.turns().Run(ctx, TurnRequest{
		Session:       session,
		ChannelID:     msg.ChannelID,
		UserID:        msg.UserID,
		Text:          msg.Text,
		StoreText:     textToStore,
		StoreMetadata: msg.Metadata,
		Attachments:   aiAttachments,
		Origin: &approval.Origin{ // conduit-31jg.43: live human turn on a promptable channel
			Source: msg.ChannelID, ChannelID: msg.ChannelID, UserID: msg.UserID,
			SessionKey: session.Key, Notify: notify,
		},
	}, newChannelTurnSink(g, msg, session))
}
