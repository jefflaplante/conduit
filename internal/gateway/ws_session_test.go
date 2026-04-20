package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/protocol"
)

func TestHandleWebSocketSessionSwitch_Create(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()
	c := newTestWSClient("c1")
	c.UserID = "jeff"

	msg := &protocol.SessionSwitch{Action: "create"}
	gw.handleWebSocketSessionSwitch(c, msg)
	out := drainClientMessage(t, c)
	if out["action"] != "created" {
		t.Errorf("expected action=created, got %v", out["action"])
	}
	if c.SessionKey == "" {
		t.Error("expected SessionKey to be set")
	}
}

func TestHandleWebSocketSessionSwitch_SwitchMissingKey(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	c := newTestWSClient("c1")
	c.UserID = "jeff"
	msg := &protocol.SessionSwitch{Action: "switch"}
	gw.handleWebSocketSessionSwitch(c, msg)
	out := drainClientMessage(t, c)
	if out["code"] != "invalid_request" {
		t.Errorf("expected invalid_request, got %v", out["code"])
	}
}

func TestHandleWebSocketSessionSwitch_SwitchNotFound(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	c := newTestWSClient("c1")
	c.UserID = "jeff"
	msg := &protocol.SessionSwitch{Action: "switch", SessionKey: "no-such"}
	gw.handleWebSocketSessionSwitch(c, msg)
	out := drainClientMessage(t, c)
	if out["code"] != "session_error" {
		t.Errorf("expected session_error, got %v", out["code"])
	}
}

func TestHandleWebSocketSessionSwitch_SwitchSuccess(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	_, _ = store.AddMessage(sess.Key, "user", "hi", nil)

	c := newTestWSClient("c1")
	c.UserID = "jeff"
	msg := &protocol.SessionSwitch{Action: "switch", SessionKey: sess.Key}
	gw.handleWebSocketSessionSwitch(c, msg)
	out := drainClientMessage(t, c)
	if out["action"] != "switched" {
		t.Errorf("expected action=switched, got %v", out["action"])
	}
	if c.SessionKey != sess.Key {
		t.Errorf("expected SessionKey updated, got %q", c.SessionKey)
	}
}

func TestHandleWebSocketSessionSwitch_List(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	_, _ = store.GetOrCreateSession("jeff", "tui_jeff")
	_, _ = store.GetOrCreateSession("jeff", "telegram_jeff")
	_, _ = store.GetOrCreateSession("jeff", "ssh_jeff")

	c := newTestWSClient("c1")
	c.UserID = "jeff"
	msg := &protocol.SessionSwitch{Action: "list"}
	gw.handleWebSocketSessionSwitch(c, msg)
	out := drainClientMessage(t, c)
	if out["action"] != "list" {
		t.Errorf("expected action=list, got %v", out["action"])
	}
}

func TestHandleWebSocketSessionSwitch_UnknownAction(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	c := newTestWSClient("c1")
	c.UserID = "jeff"
	msg := &protocol.SessionSwitch{Action: "invalid"}
	gw.handleWebSocketSessionSwitch(c, msg)
	out := drainClientMessage(t, c)
	if out["code"] != "invalid_request" {
		t.Errorf("expected invalid_request, got %v", out["code"])
	}
	m, _ := out["message"].(string)
	if !strings.Contains(m, "Unknown session action") {
		t.Errorf("expected 'Unknown session action', got %q", m)
	}
}

func TestChannelStatusAdapter_GetStatus(t *testing.T) {
	m := channels.NewManager()
	if err := m.Start(context.Background(), nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	a := &channelStatusAdapter{manager: m}
	status := a.GetStatus()
	if status == nil {
		t.Error("expected non-nil status map")
	}
	// Empty manager -> empty map
	if len(status) != 0 {
		t.Errorf("expected empty, got %v", status)
	}
}

func TestStartStopChannels(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	gw.config = &config.Config{Channels: nil}
	gw.logger = newTestLogger()

	// startChannels will fail because channelManager was already started in the
	// test helper (Manager.Start can only run once). Instead, test stopChannels
	// on the already-started manager.
	gw.stopChannels()
}

func TestStartChannels_FreshManager(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()
	// Replace with a fresh (unstarted) channel manager
	gw.channelManager = channels.NewManager()
	gw.config = &config.Config{Channels: []config.ChannelConfig{
		{Name: "disabled", Type: "telegram", Enabled: false},
	}}

	if err := gw.startChannels(context.Background()); err != nil {
		t.Fatalf("startChannels: %v", err)
	}
	gw.stopChannels()
}

func TestProcessMessages_ContextCancelled(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()
	gw.msgSemaphore = make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		gw.processMessages(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("processMessages did not return after ctx cancel")
	}
}
