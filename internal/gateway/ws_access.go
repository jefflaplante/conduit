package gateway

import (
	"conduit/internal/approval"
	"conduit/internal/sessions"
)

// WebSocket access control by token role (conduit-31jg.67, conduit-31jg.55).
//
// Any valid token used to count as the human: it could open any session by
// key (including Telegram history), act as any user ID from the message,
// and approve owner actions — together that let an automation token approve
// an owner-account email send on the owner's Telegram session. Owner-role
// tokens keep that reach; every other client is confined to sessions whose
// user is its own token identity, is pinned to that identity, and runs
// non-interactive turns that can neither prompt for nor grant approval.

// wsAutomationSource labels non-owner WebSocket turns for approvals and logs.
const wsAutomationSource = "websocket_automation"

// wsIdentity is the user ID a client acts as when it names none.
func wsIdentity(client *Client) string {
	if client.UserID != "" {
		return client.UserID
	}
	return client.Role // fall back to client name
}

// wsUserID resolves the user ID for a request that may name one. Owners may
// act as any user; other clients may only name their own identity. ok is
// false when a non-owner names someone else.
func wsUserID(client *Client, requested string) (userID string, ok bool) {
	own := wsIdentity(client)
	if requested == "" || requested == own {
		return own, true
	}
	if client.Owner {
		return requested, true
	}
	return "", false
}

// wsCanAccessSession reports whether client may read or act on s.
func wsCanAccessSession(client *Client, s *sessions.Session) bool {
	return client.Owner || (s != nil && s.UserID == wsIdentity(client))
}

// wsTurnOrigin returns how a turn started by client is marked for
// approvals: interactive (the client is prompted and may approve) for
// owners, non-interactive for everyone else so owner-account actions fail
// closed instead of prompting a script.
func (g *Gateway) wsTurnOrigin(client *Client, session *sessions.Session, userID string) (*approval.Origin, string) {
	if !client.Owner {
		return nil, wsAutomationSource
	}
	return &approval.Origin{
		Source: "websocket", ChannelID: session.ChannelID, UserID: userID,
		SessionKey: session.Key, Notify: g.wsApprovalNotifier(client, session.Key),
	}, ""
}
