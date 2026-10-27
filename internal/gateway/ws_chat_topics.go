package gateway

import (
	"fmt"
	"time"

	"conduit/internal/protocol"
	"conduit/internal/sessions"
)

// Topic routing on the websocket ingress (conduit-tui-topics-c90u): a WS
// client's chat is "tui_<userID>" + userID, the identity its home sessions
// already use, and the chat's topic pointer picks the session the same way
// channel ingress does (resolveSessionForChat).

// wsChatChannel returns the channel ID of a WS user's chat.
func wsChatChannel(userID string) string { return fmt.Sprintf("tui_%s", userID) }

// wsFollowsTopicPointer reports whether a WS message addressed to existing
// (nil when no key was given or the key is unknown) goes where the chat's
// topic pointer says. It does for no session, for the chat's home session
// (its latest non-topic session, what GetOrCreateSession returns) and, when
// the key is the connection's remembered one rather than one the client
// sent, for topic sessions, so /topic and /home move a keyless client.
// Any other session the client names explicitly wins (TUI tabs).
func (g *Gateway) wsFollowsTopicPointer(existing *sessions.Session, explicit bool, channelID, userID string) bool {
	if existing == nil {
		return true
	}
	if isTopicSessionKey(existing.Key) {
		return !explicit
	}
	if existing.ChannelID != channelID || existing.UserID != userID {
		return false
	}
	home, err := g.sessions.GetLatestSession(userID, channelID)
	return err == nil && home.Key == existing.Key
}

// sendWSNotice delivers a gateway notice (e.g. the stale-topic reset) to a
// WS client as a command response.
func (g *Gateway) sendWSNotice(client *Client, sessionKey, notice string) {
	g.sendToClient(client, &protocol.CommandResponse{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeCommandResponse,
			ID:        fmt.Sprintf("cr_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: sessionKey,
		Command:    "/topic",
		Response:   notice,
	})
}
