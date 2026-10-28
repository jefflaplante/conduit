package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/topics"
)

// conduit-tui-topics-c90u: topic routing and /topic over the websocket path.

const (
	wsUser    = "u1"
	wsChannel = "tui_u1"
)

// drainAll returns every message queued on the client.
func drainAll(t *testing.T, c *Client) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for {
		select {
		case data := <-c.Send:
			var m map[string]interface{}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

// wsChat sends text over handleWebSocketChat and returns what the client got.
func wsChat(t *testing.T, gw *Gateway, c *Client, sessionKey, text string) []map[string]interface{} {
	t.Helper()
	gw.handleWebSocketChat(context.Background(), c, &protocol.ChatMessage{SessionKey: sessionKey, UserID: wsUser, Text: text})
	return drainAll(t, c)
}

// wsChatSession sends a plain message and returns the session it ran on:
// with no AI configured the turn stops at the ai_error carrying the
// resolved session key.
func wsChatSession(t *testing.T, gw *Gateway, c *Client, sessionKey string) string {
	t.Helper()
	for _, m := range wsChat(t, gw, c, sessionKey, "hello") {
		if m["code"] == "ai_error" {
			key, _ := m["session_key"].(string)
			return key
		}
	}
	t.Fatal("no ai_error response")
	return ""
}

func wsResponses(msgs []map[string]interface{}) []string {
	var out []string
	for _, m := range msgs {
		if r, ok := m["response"].(string); ok {
			out = append(out, r)
		}
	}
	return out
}

func wsTopic(t *testing.T, gw *Gateway, slug string) *topics.Topic {
	t.Helper()
	tp, err := gw.topicStore().Get(slug)
	if err != nil {
		t.Fatalf("topic %s: %v", slug, err)
	}
	return tp
}

func newWSTopicGateway(t *testing.T) (*Gateway, *sessions.Store, *sessions.Session) {
	t.Helper()
	gw, store := newTopicTestGateway(t)
	home, err := store.GetOrCreateSession(wsUser, wsChannel)
	if err != nil {
		t.Fatal(err)
	}
	return gw, store, home
}

// (a) The chat's pointer picks the session for keyless clients, the
// connection's remembered key and the explicit home key; /home returns.
func TestWSChat_TopicPointerRoutesSession(t *testing.T) {
	gw, _, home := newWSTopicGateway(t)
	c := newTestWSClient("c1")

	if got := wsChatSession(t, gw, c, ""); got != home.Key {
		t.Fatalf("no pointer: session %s, want home %s", got, home.Key)
	}

	reply := strings.Join(wsResponses(wsChat(t, gw, c, "", "/topic alpha")), "\n")
	if !strings.Contains(reply, "[alpha]") {
		t.Fatalf("/topic alpha reply = %q", reply)
	}
	alpha := wsTopic(t, gw, "alpha")

	if got := wsChatSession(t, gw, c, ""); got != alpha.SessionKey {
		t.Errorf("remembered home key: session %s, want topic %s", got, alpha.SessionKey)
	}
	if got := wsChatSession(t, gw, newTestWSClient("c2"), ""); got != alpha.SessionKey {
		t.Errorf("keyless client: session %s, want topic %s", got, alpha.SessionKey)
	}
	if got := wsChatSession(t, gw, newTestWSClient("c3"), home.Key); got != alpha.SessionKey {
		t.Errorf("explicit home key: session %s, want topic %s", got, alpha.SessionKey)
	}

	// c now remembers the topic session; /home moves it back.
	wsChat(t, gw, c, "", "/home")
	if got := wsChatSession(t, gw, c, ""); got != home.Key {
		t.Errorf("after /home: session %s, want home %s", got, home.Key)
	}
}

// The CommandMessage path (TUI slash commands) uses the same chat: the
// identity comes from the addressed session, so a pointer set there routes
// the next chat message.
func TestWSCommand_TopicUsesChatIdentity(t *testing.T) {
	gw, _, home := newWSTopicGateway(t)
	c := newTestWSClient("c1") // identity "client", acting as u1 via the session

	gw.handleWebSocketCommand(context.Background(), c, &protocol.CommandMessage{SessionKey: home.Key, Command: "/topic", Args: "beta"})
	reply := strings.Join(wsResponses(drainAll(t, c)), "\n")
	if !strings.Contains(reply, "[beta]") {
		t.Fatalf("/topic beta reply = %q", reply)
	}
	beta := wsTopic(t, gw, "beta")
	if got := wsChatSession(t, gw, c, home.Key); got != beta.SessionKey {
		t.Fatalf("after /topic beta command: session %s, want %s", got, beta.SessionKey)
	}

	gw.handleWebSocketCommand(context.Background(), c, &protocol.CommandMessage{SessionKey: home.Key, Command: "/home"})
	drainAll(t, c)
	if got := wsChatSession(t, gw, c, home.Key); got != home.Key {
		t.Fatalf("after /home command: session %s, want home %s", got, home.Key)
	}
}

// (b) An explicit key naming anything but the chat's home session wins
// over the pointer: other TUI tabs and an explicitly addressed topic.
func TestWSChat_ExplicitKeyWinsOverPointer(t *testing.T) {
	gw, store, _ := newWSTopicGateway(t)
	c := newTestWSClient("c1")
	tab, err := store.GetOrCreateSession(wsUser, "tui_u1_123")
	if err != nil {
		t.Fatal(err)
	}
	wsChat(t, gw, c, "", "/topic beta")
	beta := wsTopic(t, gw, "beta")
	wsChat(t, gw, c, "", "/topic alpha")
	alpha := wsTopic(t, gw, "alpha")
	if got := wsChatSession(t, gw, c, ""); got != alpha.SessionKey {
		t.Fatalf("pointer: session %s, want %s", got, alpha.SessionKey)
	}

	for name, key := range map[string]string{"tab": tab.Key, "topic": beta.SessionKey} {
		if got := wsChatSession(t, gw, newTestWSClient("c-"+name), key); got != key {
			t.Errorf("explicit %s key: session %s, want %s", name, got, key)
		}
	}
	if p, _ := gw.topicStore().Pointer(topicChatKey(wsChannel, wsUser)); p != "alpha" {
		t.Errorf("explicit keys must not move the pointer, got %q", p)
	}
}

// (c) A pointer to an archived topic resets to home with a one-time notice
// delivered as a command response.
func TestWSChat_StalePointerNoticeAndHome(t *testing.T) {
	gw, _, home := newWSTopicGateway(t)
	c := newTestWSClient("c1")
	wsChat(t, gw, c, "", "/topic alpha")
	if err := gw.topicStore().SetStatus("alpha", topics.StatusArchived); err != nil {
		t.Fatal(err)
	}

	msgs := wsChat(t, gw, c, "", "hello")
	var notice map[string]interface{}
	var errKey string
	for _, m := range msgs {
		if r, _ := m["response"].(string); strings.Contains(r, "no longer active") {
			notice = m
		}
		if m["code"] == "ai_error" {
			errKey, _ = m["session_key"].(string)
		}
	}
	if notice == nil || notice["type"] != string(protocol.TypeCommandResponse) {
		t.Fatalf("want a command_response notice, got %v", msgs)
	}
	if notice["session_key"] != home.Key || errKey != home.Key {
		t.Fatalf("stale pointer: notice on %v, turn on %s, want home %s", notice["session_key"], errKey, home.Key)
	}

	for _, r := range wsResponses(wsChat(t, gw, c, "", "hello")) {
		if strings.Contains(r, "no longer active") {
			t.Fatalf("notice repeated: %q", r)
		}
	}
}

func TestHandleWebSocketCommandFromChat_HelpListsTopics(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, wsChannel, wsUser, "", "/help")
	r, _ := drainClientMessage(t, c)["response"].(string)
	for _, want := range []string{"/topic", "/home"} {
		if !strings.Contains(r, want) {
			t.Errorf("help missing %s: %q", want, r)
		}
	}
}
