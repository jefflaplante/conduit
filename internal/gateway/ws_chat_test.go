package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/protocol"
)

// newTestWSClient returns a Client with a buffered Send channel so sendToClient
// and sendErrorToClient can be inspected by the test.
func newTestWSClient(id string) *Client {
	return &Client{
		ID:   id,
		Role: "client",
		Send: make(chan []byte, 32),
	}
}

func drainClientMessage(t *testing.T, c *Client) map[string]interface{} {
	t.Helper()
	select {
	case data := <-c.Send:
		var out map[string]interface{}
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out
	default:
		t.Fatal("no message in client Send channel")
		return nil
	}
}

func TestSendToClient_MarshalAndEnqueue(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	c := newTestWSClient("c1")
	msg := &protocol.CommandResponse{
		BaseMessage: protocol.BaseMessage{Type: protocol.TypeCommandResponse, ID: "id-1"},
		SessionKey:  "sk",
		Command:     "/test",
		Response:    "ok",
	}
	gw.sendToClient(c, msg)
	out := drainClientMessage(t, c)
	if out["command"] != "/test" {
		t.Errorf("expected command=/test, got %v", out["command"])
	}
	if out["response"] != "ok" {
		t.Errorf("expected response=ok, got %v", out["response"])
	}
}

func TestSendToClient_BufferFull(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	c := &Client{ID: "c1", Role: "client", Send: make(chan []byte, 1)}
	// Fill buffer
	c.Send <- []byte("{}")
	// Drop silently
	gw.sendToClient(c, &protocol.CommandResponse{
		BaseMessage: protocol.BaseMessage{Type: protocol.TypeCommandResponse},
	})
	// No panic; the dropped message isn't in the channel
}

func TestSendErrorToClient(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	c := newTestWSClient("c1")
	gw.sendErrorToClient(c, "sess-1", "bad_request", "nope")
	out := drainClientMessage(t, c)
	if out["code"] != "bad_request" {
		t.Errorf("expected code, got %v", out["code"])
	}
	if out["message"] != "nope" {
		t.Errorf("expected message, got %v", out["message"])
	}
	if out["session_key"] != "sess-1" {
		t.Errorf("expected session_key, got %v", out["session_key"])
	}
}

func TestHandleWebSocketCommand_NoSession_Status(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	c := newTestWSClient("c1")
	cmdMsg := &protocol.CommandMessage{Command: "/status", Args: "", SessionKey: ""}
	gw.handleWebSocketCommand(context.Background(), c, cmdMsg)

	out := drainClientMessage(t, c)
	if out["command"] != "/status" {
		t.Errorf("expected /status, got %v", out["command"])
	}
	r, ok := out["response"].(string)
	if !ok || !strings.Contains(r, "No active session") {
		t.Errorf("expected 'No active session', got %v", out["response"])
	}
}

func TestHandleWebSocketCommand_AddsSlashPrefix(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	c := newTestWSClient("c1")
	// Missing leading slash, no args
	cmdMsg := &protocol.CommandMessage{Command: "help", Args: "", SessionKey: ""}
	gw.handleWebSocketCommand(context.Background(), c, cmdMsg)
	out := drainClientMessage(t, c)
	if out["command"] != "/help" {
		t.Errorf("expected /help, got %v", out["command"])
	}
}

func TestHandleWebSocketCommandFromChat_Help(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, "", "/help")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "/reset") {
		t.Errorf("expected help mentions /reset, got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Commands(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, "", "/commands")
	drainClientMessage(t, c)
}

func TestHandleWebSocketCommandFromChat_Unknown(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, "sess", "/nope")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "Unknown") {
		t.Errorf("expected Unknown command, got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Stop_NoActive(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	gw.activeRequests = make(map[string]context.CancelFunc)
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, "sess-1", "/stop")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "No active") {
		t.Errorf("expected 'No active', got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Stop_Active(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	gw.activeRequests = make(map[string]context.CancelFunc)
	called := false
	gw.activeRequests["sess-1"] = func() { called = true }
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, "sess-1", "/stop")
	drainClientMessage(t, c)
	if !called {
		t.Error("expected cancel called")
	}
}

func TestHandleWebSocketCommandFromChat_Reset_NoSession(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, "", "/reset")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "No active session") {
		t.Errorf("expected 'No active session', got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Reset_WithSession(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	_, _ = store.AddMessage(sess.Key, "user", "hi", nil)
	_ = store.SetSessionContext(sess.Key, "last_prompt_tokens", "500")

	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/reset")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "Session reset") {
		t.Errorf("expected 'Session reset', got %q", r)
	}

	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["last_prompt_tokens"] != "" {
		t.Error("expected tokens cleared")
	}
}

func TestHandleWebSocketCommandFromChat_Status(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/status")
	drainClientMessage(t, c)
}

func TestHandleWebSocketCommandFromChat_Context(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/context")
	drainClientMessage(t, c)
}

func TestHandleWebSocketCommandFromChat_Cost(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/cost")
	drainClientMessage(t, c)
}

func TestHandleWebSocketCommandFromChat_Provider_List(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/provider")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "Current Provider") {
		t.Errorf("expected 'Current Provider', got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Provider_Unknown(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/provider bogus")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "Unknown provider") {
		t.Errorf("expected 'Unknown provider', got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Provider_Switch(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/provider testprov")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "Switched to provider") {
		t.Errorf("expected 'Switched to provider', got %q", r)
	}
	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["provider"] != "testprov" {
		t.Errorf("expected provider=testprov, got %q", refreshed.Context["provider"])
	}
}

func TestHandleWebSocketCommandFromChat_Model_List(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/model")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "Current Model") {
		t.Errorf("expected 'Current Model', got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Model_KnownAlias(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/model sonnet")
	drainClientMessage(t, c)
	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("expected sonnet full, got %q", refreshed.Context["model"])
	}
}

func TestHandleWebSocketCommandFromChat_Model_RawName(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/model claude-haiku-4-5-20251001")
	drainClientMessage(t, c)
	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("expected claude-haiku-4-5-20251001, got %q", refreshed.Context["model"])
	}
}

func TestHandleWebSocketCommandFromChat_Model_UnknownShort(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/model xx")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "Unknown model") {
		t.Errorf("expected 'Unknown model', got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_SmartRoute_Status(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	gw.config.AI.SmartRouting = &config.SmartRoutingConfig{Enabled: true, CostBudgetDaily: 5}
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/smartroute")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "Smart Routing") {
		t.Errorf("expected 'Smart Routing', got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_SmartRoute_OnOff(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")

	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/smartroute on")
	drainClientMessage(t, c)
	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["smart_routing_enabled"] != "true" {
		t.Error("expected smart_routing_enabled=true")
	}

	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/smartroute off")
	drainClientMessage(t, c)
	refreshed, _ = store.GetSession(sess.Key)
	if refreshed.Context["smart_routing_enabled"] != "false" {
		t.Error("expected smart_routing_enabled=false")
	}
}

func TestHandleWebSocketCommandFromChat_SmartRoute_Budget(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")

	// Missing amount
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/smartroute budget")
	drainClientMessage(t, c)

	// Invalid
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/smartroute budget abc")
	drainClientMessage(t, c)

	// Valid
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/smartroute budget 3.25")
	drainClientMessage(t, c)
	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["smart_routing_budget"] != "3.25" {
		t.Errorf("expected budget=3.25, got %q", refreshed.Context["smart_routing_budget"])
	}
}

func TestHandleWebSocketCommandFromChat_SmartRoute_Unknown(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/smartroute bogus")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "Usage:") {
		t.Errorf("expected Usage help, got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Compact_NoEngine(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/compact")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "not configured") {
		t.Errorf("expected 'not configured', got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Compact_NoSession(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, "", "/compact")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "No active session") {
		t.Errorf("expected 'No active session', got %q", r)
	}
}

func TestHandleWebSocketCommandFromChat_Goodbye_NoSession(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, "", "/goodbye")
	out := drainClientMessage(t, c)
	r, _ := out["response"].(string)
	if !strings.Contains(r, "No active session") {
		t.Errorf("expected 'No active session', got %q", r)
	}
}
