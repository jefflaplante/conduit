package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/tui"
)

func newTestDirectClient(t *testing.T) (*DirectClient, *sessions.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "dc.db")
	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Construct a Router with no providers (allowed for testing) so
	// that DirectClient paths which touch router metadata don't panic.
	router, err := ai.NewRouter(config.AIConfig{}, nil)
	if err != nil {
		t.Fatalf("ai.NewRouter: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := NewDirectClient(DirectClientConfig{
		ParentCtx: ctx,
		UserID:    "jeff",
		Sessions:  store,
		AI:        router,
		AgentName: "test-agent",
		Version:   "v0.0.1",
		GitCommit: "deadbeef",
		ModelAliases: map[string]string{
			"sonnet": "claude-sonnet-4-20250514",
			"haiku":  "claude-haiku-4-5-20251001",
		},
		ToolCount:  10,
		SkillCount: 5,
		UptimeFunc: func() int64 { return 42 },
	})
	t.Cleanup(c.Close)
	return c, store
}

func drainOne(t *testing.T, c *DirectClient, timeout time.Duration) tea.Msg {
	t.Helper()
	select {
	case m := <-c.inbox:
		return m
	case <-time.After(timeout):
		t.Fatal("timed out waiting for message")
		return nil
	}
}

func TestDirectClient_ConnectCmd(t *testing.T) {
	c, _ := newTestDirectClient(t)

	cmd := c.ConnectCmd()
	msg := cmd() // execute the command
	if _, ok := msg.(tui.ConnectedMsg); !ok {
		t.Errorf("expected ConnectedMsg, got %T", msg)
	}
	// Also drain the GatewayInfoMsg that was enqueued
	info := drainOne(t, c, time.Second)
	gi, ok := info.(tui.GatewayInfoMsg)
	if !ok {
		t.Fatalf("expected GatewayInfoMsg, got %T", info)
	}
	if gi.AssistantName != "test-agent" {
		t.Errorf("unexpected name: %q", gi.AssistantName)
	}
	if gi.ToolCount != 10 || gi.SkillCount != 5 {
		t.Errorf("unexpected counts: tool=%d, skill=%d", gi.ToolCount, gi.SkillCount)
	}
	if gi.UptimeSeconds != 42 {
		t.Errorf("unexpected uptime: %d", gi.UptimeSeconds)
	}
}

func TestDirectClient_ReconnectCmd(t *testing.T) {
	c, _ := newTestDirectClient(t)
	cmd := c.ReconnectCmd(3)
	msg := cmd()
	if _, ok := msg.(tui.ConnectedMsg); !ok {
		t.Errorf("expected ConnectedMsg, got %T", msg)
	}
}

func TestDirectClient_IsConnected(t *testing.T) {
	c, _ := newTestDirectClient(t)
	if !c.IsConnected() {
		t.Error("expected IsConnected true after construction")
	}
	c.Close()
	if c.IsConnected() {
		t.Error("expected IsConnected false after Close")
	}
}

func TestDirectClient_ListenCmd_ContextDone(t *testing.T) {
	c, _ := newTestDirectClient(t)
	c.Close()

	cmd := c.ListenCmd()
	done := make(chan tea.Msg, 1)
	go func() {
		done <- cmd()
	}()
	select {
	case m := <-done:
		if _, ok := m.(tui.DisconnectedMsg); !ok {
			t.Errorf("expected DisconnectedMsg, got %T", m)
		}
	case <-time.After(time.Second):
		t.Fatal("ListenCmd did not return after Close")
	}
}

func TestDirectClient_ListenCmd_ReceivesMessage(t *testing.T) {
	c, _ := newTestDirectClient(t)
	// Enqueue a message
	c.send(tui.StreamDeltaMsg{Delta: "hi"})

	cmd := c.ListenCmd()
	msg := cmd()
	d, ok := msg.(tui.StreamDeltaMsg)
	if !ok {
		t.Fatalf("expected StreamDeltaMsg, got %T", msg)
	}
	if d.Delta != "hi" {
		t.Errorf("expected delta 'hi', got %q", d.Delta)
	}
}

func TestDirectClient_CreateSession(t *testing.T) {
	c, _ := newTestDirectClient(t)

	if err := c.CreateSession(); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	msg := drainOne(t, c, time.Second)
	sc, ok := msg.(tui.SessionCreatedMsg)
	if !ok {
		t.Fatalf("expected SessionCreatedMsg, got %T", msg)
	}
	if sc.Key == "" {
		t.Error("expected session key to be set")
	}
}

func TestDirectClient_CreateSessionWithID(t *testing.T) {
	c, _ := newTestDirectClient(t)
	if err := c.CreateSessionWithID("req-1"); err != nil {
		t.Fatalf("CreateSessionWithID: %v", err)
	}
	msg := drainOne(t, c, time.Second)
	sc, ok := msg.(tui.SessionCreatedMsg)
	if !ok {
		t.Fatalf("expected SessionCreatedMsg, got %T", msg)
	}
	if sc.RequestID != "req-1" {
		t.Errorf("expected req-1, got %q", sc.RequestID)
	}
}

func TestDirectClient_SwitchSession_NotFound(t *testing.T) {
	c, _ := newTestDirectClient(t)
	err := c.SwitchSession("no-such-key")
	if err == nil {
		t.Error("expected error for missing session")
	}
	// Should have emitted an error message
	msg := drainOne(t, c, time.Second)
	if _, ok := msg.(tui.ErrorMsg); !ok {
		t.Errorf("expected ErrorMsg, got %T", msg)
	}
}

func TestDirectClient_SwitchSession_Success(t *testing.T) {
	c, store := newTestDirectClient(t)
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	_, _ = store.AddMessage(sess.Key, "user", "hello", nil)
	_, _ = store.AddMessage(sess.Key, "assistant", "world", nil)

	if err := c.SwitchSession(sess.Key); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	msg := drainOne(t, c, time.Second)
	ss, ok := msg.(tui.SessionSwitchedMsg)
	if !ok {
		t.Fatalf("expected SessionSwitchedMsg, got %T", msg)
	}
	if ss.Key != sess.Key {
		t.Errorf("key mismatch: %q", ss.Key)
	}
	if len(ss.History) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(ss.History))
	}
}

func TestDirectClient_ListSessions(t *testing.T) {
	c, store := newTestDirectClient(t)
	// Create sessions with various origins
	s1, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	s2, _ := store.GetOrCreateSession("jeff", "telegram_jeff")
	_ = s1
	_ = s2

	if err := c.ListSessions(); err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	msg := drainOne(t, c, time.Second)
	sl, ok := msg.(tui.SessionListMsg)
	if !ok {
		t.Fatalf("expected SessionListMsg, got %T", msg)
	}
	if len(sl.Sessions) < 2 {
		t.Errorf("expected >=2 sessions, got %d", len(sl.Sessions))
	}
	// Check origins are set
	foundTUI, foundTG := false, false
	for _, s := range sl.Sessions {
		if s.Metadata["origin"] == "TUI" {
			foundTUI = true
		}
		if s.Metadata["origin"] == "TG" {
			foundTG = true
		}
	}
	if !foundTUI {
		t.Error("expected a TUI-origin session")
	}
	if !foundTG {
		t.Error("expected a TG-origin session")
	}
}

func TestFormatToolArgs_Empty(t *testing.T) {
	if s := formatToolArgs(nil); s != "" {
		t.Errorf("expected empty, got %q", s)
	}
	if s := formatToolArgs(map[string]interface{}{}); s != "" {
		t.Errorf("expected empty, got %q", s)
	}
}

func TestFormatToolArgs_Basic(t *testing.T) {
	out := formatToolArgs(map[string]interface{}{"k": "v"})
	if out != "k=v" {
		t.Errorf("expected 'k=v', got %q", out)
	}
}

func TestFormatToolArgs_LongValueTruncated(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	out := formatToolArgs(map[string]interface{}{"k": long})
	if len(out) > 70 {
		// Expected: "k=" (2) + 57 + "..." (3) = 62
		t.Errorf("expected truncated value, got %d chars: %q", len(out), out)
	}
	if out[len(out)-3:] != "..." {
		t.Errorf("expected trailing ..., got %q", out)
	}
}

func TestDirectClient_SendChat_CommandPath(t *testing.T) {
	c, store := newTestDirectClient(t)
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")

	// Sending a slash command should go through handleCommand path
	if err := c.SendChat(sess.Key, "/help"); err != nil {
		t.Fatalf("SendChat /help: %v", err)
	}
	msg := drainOne(t, c, time.Second)
	cr, ok := msg.(tui.CommandResponseMsg)
	if !ok {
		t.Fatalf("expected CommandResponseMsg, got %T", msg)
	}
	if cr.Command != "/help" {
		t.Errorf("expected /help, got %q", cr.Command)
	}
}

func TestDirectClient_HandleCommand_Reset_NoSession(t *testing.T) {
	c, _ := newTestDirectClient(t)
	c.handleCommand("", "/reset")
	msg := drainOne(t, c, time.Second)
	if _, ok := msg.(tui.CommandResponseMsg); !ok {
		t.Fatalf("expected CommandResponseMsg, got %T", msg)
	}
}

func TestDirectClient_HandleCommand_Reset(t *testing.T) {
	c, store := newTestDirectClient(t)
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	_, _ = store.AddMessage(sess.Key, "user", "hi", nil)
	_ = store.SetSessionContext(sess.Key, "last_prompt_tokens", "500")

	c.handleCommand(sess.Key, "/reset")
	msg := drainOne(t, c, time.Second)
	cr, ok := msg.(tui.CommandResponseMsg)
	if !ok {
		t.Fatalf("expected CommandResponseMsg, got %T", msg)
	}
	if cr.Response == "" {
		t.Error("expected non-empty response")
	}

	refreshed, _ := store.GetSession(sess.Key)
	if refreshed.Context["last_prompt_tokens"] != "" {
		t.Errorf("expected context cleared, got %q", refreshed.Context["last_prompt_tokens"])
	}
}

func TestDirectClient_HandleCommand_Help(t *testing.T) {
	c, _ := newTestDirectClient(t)
	c.handleCommand("any", "/help")
	msg := drainOne(t, c, time.Second)
	cr, ok := msg.(tui.CommandResponseMsg)
	if !ok {
		t.Fatalf("expected CommandResponseMsg, got %T", msg)
	}
	if cr.Response == "" {
		t.Error("expected non-empty help")
	}
}

func TestDirectClient_HandleCommand_Status_NoSession(t *testing.T) {
	c, _ := newTestDirectClient(t)
	c.handleCommand("", "/status")
	msg := drainOne(t, c, time.Second)
	if _, ok := msg.(tui.CommandResponseMsg); !ok {
		t.Fatalf("expected CommandResponseMsg, got %T", msg)
	}
}

func TestDirectClient_HandleCommand_Status(t *testing.T) {
	c, store := newTestDirectClient(t)
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	c.handleCommand(sess.Key, "/status")
	drainOne(t, c, time.Second)
}

func TestDirectClient_HandleCommand_Context(t *testing.T) {
	c, store := newTestDirectClient(t)
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	c.handleCommand(sess.Key, "/context")
	drainOne(t, c, time.Second)
}

func TestDirectClient_HandleCommand_Cost(t *testing.T) {
	c, store := newTestDirectClient(t)
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	c.handleCommand(sess.Key, "/cost")
	drainOne(t, c, time.Second)
}

func TestDirectClient_HandleCommand_Unknown(t *testing.T) {
	c, _ := newTestDirectClient(t)
	c.handleCommand("any", "/notacommand")
	msg := drainOne(t, c, time.Second)
	cr, ok := msg.(tui.CommandResponseMsg)
	if !ok {
		t.Fatalf("expected CommandResponseMsg, got %T", msg)
	}
	// Should mention "Unknown command"
	if cr.Response == "" {
		t.Error("expected non-empty response")
	}
}

func TestDirectClient_HandleCommand_Stop_NoActive(t *testing.T) {
	c, _ := newTestDirectClient(t)
	c.handleCommand("sess-1", "/stop")
	drainOne(t, c, time.Second)
}

func TestDirectClient_HandleCommand_Stop_Active(t *testing.T) {
	c, _ := newTestDirectClient(t)
	called := false
	c.activeRequestsMu.Lock()
	c.activeRequests["sess-1"] = func() { called = true }
	c.activeRequestsMu.Unlock()

	c.handleCommand("sess-1", "/stop")
	drainOne(t, c, time.Second)
	if !called {
		t.Error("expected cancel function invoked")
	}
}

func TestDirectClient_SendCommand_AddsPrefixAndArgs(t *testing.T) {
	c, _ := newTestDirectClient(t)
	// No leading slash, with args -> "/help args"
	if err := c.SendCommand("any", "help", "me"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	msg := drainOne(t, c, time.Second)
	cr, ok := msg.(tui.CommandResponseMsg)
	if !ok {
		t.Fatalf("expected CommandResponseMsg, got %T", msg)
	}
	// Command field was first token of text
	if cr.Command != "/help" {
		t.Errorf("expected /help, got %q", cr.Command)
	}
}
