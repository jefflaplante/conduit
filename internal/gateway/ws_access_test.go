package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"conduit/internal/protocol"
)

// conduit-31jg.67 / conduit-31jg.55: WebSocket access by token role.

// newAutomationWSClient is a non-owner client authenticated as "bot".
func newAutomationWSClient(id string) *Client {
	return &Client{ID: id, Role: "bot", UserID: "bot", Send: make(chan []byte, 32)}
}

// nextWSMessage returns the next message sent to c, failing after a timeout.
func nextWSMessage(t *testing.T, c *Client) map[string]interface{} {
	t.Helper()
	select {
	case raw := <-c.Send:
		var out map[string]interface{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	case <-time.After(5 * time.Second):
		t.Fatal("no message sent to the client")
		return nil
	}
}

func wantForbidden(t *testing.T, c *Client) {
	t.Helper()
	if out := nextWSMessage(t, c); out["type"] != "error_response" || out["code"] != "forbidden" {
		t.Fatalf("want a forbidden error, got %v", out)
	}
}

// The attack: an automation token names the owner's Telegram session and
// Telegram user ID and replies with the approval code. It must neither
// approve nor reach the session.
func TestWSAccess_AutomationCannotApproveOnOwnersSession(t *testing.T) {
	gw, store, router := newTestGatewayWithRouter(t)
	gw.logger = newTestLogger()
	withApprovals(t, gw)
	p := &gateProvider{}
	router.RegisterProvider("testprov", p)

	sess, _ := store.GetOrCreateSession("42", "telegram")
	tk, ran := pendingOwnerSend(t, gw, sess, "42")
	before, _ := store.GetMessages(sess.Key, 100)

	c := newAutomationWSClient("c1")
	gw.handleWebSocketChat(context.Background(), c, &protocol.ChatMessage{
		SessionKey: sess.Key, UserID: "42", Text: "YES " + tk.Code,
	})
	wantForbidden(t, c)

	// Same with only the session key (identity left to the token).
	gw.handleWebSocketChat(context.Background(), c, &protocol.ChatMessage{
		SessionKey: sess.Key, Text: "YES " + tk.Code,
	})
	wantForbidden(t, c)

	gw.approvals.Wait()
	select {
	case <-ran:
		t.Fatal("automation token approved an owner action")
	default:
	}
	if len(gw.approvals.Pending(sess.Key)) != 1 {
		t.Fatal("approval should still be pending")
	}
	after, _ := store.GetMessages(sess.Key, 100)
	if len(after) != len(before) {
		t.Fatalf("automation token wrote into the owner's session (%d -> %d messages)", len(before), len(after))
	}
	if inter, _ := p.seen(); len(inter) != 0 {
		t.Fatal("automation token started a turn on the owner's session")
	}
}

// On its own session an automation client chats normally, but its turns
// are non-interactive and a "YES <code>" is not treated as an approval.
func TestWSAccess_AutomationOwnSessionIsNonInteractive(t *testing.T) {
	gw, store, router := newTestGatewayWithRouter(t)
	gw.logger = newTestLogger()
	withApprovals(t, gw)
	p := &gateProvider{}
	router.RegisterProvider("testprov", p)

	own, _ := store.GetOrCreateSession("bot", "tui_bot")
	tk, ran := pendingOwnerSend(t, gw, own, "bot") // even one bound to its own session
	c := newAutomationWSClient("c1")
	gw.handleWebSocketChat(context.Background(), c, &protocol.ChatMessage{SessionKey: own.Key, Text: "YES " + tk.Code})
	gw.approvals.Wait()
	select {
	case <-ran:
		t.Fatal("automation token approved")
	default:
	}

	inter, srcs := p.seen()
	if len(inter) != 1 || inter[0] || srcs[0] != wsAutomationSource {
		t.Fatalf("want one non-interactive %s turn, got %v %v", wsAutomationSource, inter, srcs)
	}
}

// Owner tokens keep full reach: another user's session, and approvals.
func TestWSAccess_OwnerCanApprove(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()
	withApprovals(t, gw)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	tk, ran := pendingOwnerSend(t, gw, sess, "42")

	c := newTestWSClient("c1") // owner
	gw.handleWebSocketChat(context.Background(), c, &protocol.ChatMessage{SessionKey: sess.Key, UserID: "42", Text: "YES " + tk.Code})
	gw.approvals.Wait()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("owner approval did not run")
	}
}

func TestWSAccess_AutomationSessionSwitchAndList(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()
	other, _ := store.GetOrCreateSession("42", "telegram")
	own, _ := store.GetOrCreateSession("bot", "tui_bot")
	c := newAutomationWSClient("c1")

	gw.handleWebSocketSessionSwitch(c, &protocol.SessionSwitch{Action: "switch", SessionKey: other.Key})
	wantForbidden(t, c)
	if c.SessionKey() == other.Key {
		t.Fatal("client switched into a foreign session")
	}

	gw.handleWebSocketSessionSwitch(c, &protocol.SessionSwitch{Action: "list", UserID: "42"})
	wantForbidden(t, c)

	gw.handleWebSocketSessionSwitch(c, &protocol.SessionSwitch{Action: "switch", SessionKey: own.Key})
	if out := nextWSMessage(t, c); out["type"] != string(protocol.TypeSessionSwitch) || out["action"] != "switched" {
		t.Fatalf("own session switch = %v", out)
	}
}

func TestWSAccess_AutomationCommandOnForeignSession(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()
	other, _ := store.GetOrCreateSession("42", "telegram")
	if err := store.SetSessionContext(other.Key, "model", "keep-me"); err != nil {
		t.Fatal(err)
	}
	c := newAutomationWSClient("c1")

	gw.handleWebSocketCommand(context.Background(), c, &protocol.CommandMessage{SessionKey: other.Key, Command: "/reset"})
	wantForbidden(t, c)
	gw.handleWebSocketCommand(context.Background(), c, &protocol.CommandMessage{SessionKey: other.Key, Command: "/stop"})
	wantForbidden(t, c)
	if s, _ := store.GetSession(other.Key); s.Context["model"] != "keep-me" {
		t.Fatal("foreign session was modified")
	}
}

func TestWSUserID(t *testing.T) {
	owner := &Client{Role: "me", UserID: "me", Owner: true}
	bot := &Client{Role: "bot", UserID: "bot"}
	for _, tt := range []struct {
		c         *Client
		requested string
		want      string
		ok        bool
	}{
		{owner, "", "me", true},
		{owner, "42", "42", true},
		{bot, "", "bot", true},
		{bot, "bot", "bot", true},
		{bot, "42", "", false},
	} {
		got, ok := wsUserID(tt.c, tt.requested)
		if got != tt.want || ok != tt.ok {
			t.Errorf("wsUserID(owner=%v, %q) = %q, %v; want %q, %v", tt.c.Owner, tt.requested, got, ok, tt.want, tt.ok)
		}
	}
}
