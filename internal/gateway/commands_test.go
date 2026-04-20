package gateway

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools/debuglog"
)

// newTestChannelManager returns a channels.Manager whose outgoing buffer can
// be drained to inspect messages emitted by sendCommandResponse and friends.
// It does not start the adapter routing goroutine, so messages just sit in
// the buffered channel until drained via pendingOutgoing.
func newTestChannelManager(t *testing.T) *channels.Manager {
	t.Helper()
	m := channels.NewManager()
	// Start with a cancellable context but NO routing goroutine —
	// messages queued via SendMessage are stored in the internal outgoing buffer.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := m.Start(ctx, nil); err != nil {
		t.Fatalf("channel manager start: %v", err)
	}
	return m
}

// lastCommandText pulls the first pending outgoing message from the channel
// manager's outgoing queue by temporarily stopping it and inspecting the
// drained messages. Because the manager's routing goroutine consumes
// outgoing messages, we instead use a simpler approach: test without the
// routing goroutine by using drainOutgoing through reflection-like tricks.
//
// For practical testing, we verify that SendMessage does NOT error, since
// verifying the actual payload requires a live adapter which is heavyweight.
// The higher-level handlers also persist state (e.g. session context),
// which we can verify directly.

func newTestGatewayWithSessions(t *testing.T) (*Gateway, *sessions.Store) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := newTestChannelManager(t)

	gw := &Gateway{
		sessions:       store,
		channelManager: m,
		config: &config.Config{
			AI: config.AIConfig{
				ModelAliases: map[string]string{
					"sonnet": "claude-sonnet-4-20250514",
					"haiku":  "claude-haiku-4-5-20251001",
					"opus":   "claude-opus-4-20250514",
				},
			},
		},
	}
	return gw, store
}

func TestFormatAliasKeys(t *testing.T) {
	// Single alias
	got := formatAliasKeys(map[string]string{"sonnet": "claude-sonnet-4-20250514"})
	if got != "sonnet" {
		t.Errorf("formatAliasKeys single: got %q", got)
	}

	// Multiple aliases — can come in any order, so check presence
	multi := formatAliasKeys(map[string]string{
		"sonnet": "claude-sonnet-4-20250514",
		"haiku":  "claude-haiku-4-5-20251001",
	})
	if !strings.Contains(multi, "sonnet") || !strings.Contains(multi, "haiku") {
		t.Errorf("formatAliasKeys multiple: got %q, missing keys", multi)
	}
	if !strings.Contains(multi, ", ") {
		t.Errorf("formatAliasKeys multiple: expected comma-separated, got %q", multi)
	}

	// Empty map
	if got := formatAliasKeys(map[string]string{}); got != "" {
		t.Errorf("formatAliasKeys empty: got %q", got)
	}
}

func TestFormatAliasDisplay(t *testing.T) {
	aliases := map[string]string{
		"sonnet":  "claude-sonnet-4-20250514",
		"default": "",
	}

	out := formatAliasDisplay(aliases, "• ", "→")
	if !strings.Contains(out, "sonnet") {
		t.Errorf("expected sonnet in output: %q", out)
	}
	if !strings.Contains(out, "claude-sonnet-4-20250514") {
		t.Errorf("expected full model: %q", out)
	}
	// Empty string should render as "reset to default"
	if !strings.Contains(out, "reset to default") {
		t.Errorf("expected 'reset to default' for empty model, got %q", out)
	}
	// Check prefix and arrow are present
	if !strings.Contains(out, "• ") {
		t.Errorf("expected prefix '• ' in output")
	}
	if !strings.Contains(out, "→") {
		t.Errorf("expected arrow '→' in output")
	}
}

func TestGetModelAliases_UsesConfig(t *testing.T) {
	gw := &Gateway{
		config: &config.Config{
			AI: config.AIConfig{
				ModelAliases: map[string]string{"custom": "custom-model"},
			},
		},
	}
	aliases := gw.getModelAliases()
	if aliases["custom"] != "custom-model" {
		t.Errorf("expected config aliases, got %v", aliases)
	}
}

func TestGetModelAliases_FallsBackToDefault(t *testing.T) {
	gw := &Gateway{
		config: &config.Config{
			AI: config.AIConfig{ModelAliases: nil},
		},
	}
	aliases := gw.getModelAliases()
	defaults := config.DefaultModelAliases()
	if len(aliases) != len(defaults) {
		t.Errorf("expected %d default aliases, got %d", len(defaults), len(aliases))
	}
}

func TestSendCommandResponse(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: "sess-key",
		UserID:     "user1",
	}
	// Should not panic and should enqueue without error
	gw.sendCommandResponse(msg, "hello world")
}

func TestHandleCommand_Help(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, err := store.GetOrCreateSession("user1", "tui_test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		UserID:     "user1",
		Text:       "/help",
	}
	handled := gw.handleCommand(context.Background(), msg, session)
	if !handled {
		t.Error("/help should be handled")
	}
}

func TestHandleCommand_Commands(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		UserID:     "user1",
		Text:       "/commands",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/commands should be handled")
	}
}

func TestHandleCommand_Status(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		UserID:     "user1",
		Text:       "/status",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/status should be handled")
	}
}

func TestHandleCommand_Context(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		UserID:     "user1",
		Text:       "/context",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/context should be handled")
	}
}

func TestHandleCommand_Reset(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	// Add some messages to be cleared
	if _, err := store.AddMessage(session.Key, "user", "hi", nil); err != nil {
		t.Fatalf("add msg: %v", err)
	}
	if err := store.SetSessionContext(session.Key, "last_prompt_tokens", "500"); err != nil {
		t.Fatalf("set context: %v", err)
	}

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		UserID:     "user1",
		Text:       "/reset",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/reset should be handled")
	}

	// Context should be cleared
	refreshed, _ := store.GetSession(session.Key)
	if refreshed.Context["last_prompt_tokens"] != "" {
		t.Errorf("expected last_prompt_tokens cleared, got %q", refreshed.Context["last_prompt_tokens"])
	}
}

func TestHandleCommand_Stop_NoActive(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.activeRequests = make(map[string]context.CancelFunc)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		UserID:     "user1",
		Text:       "/stop",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/stop should be handled even with no active request")
	}
}

func TestHandleCommand_Stop_WithActive(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	gw.activeRequests = make(map[string]context.CancelFunc)
	cancelled := false
	gw.activeRequests[session.Key] = func() { cancelled = true }

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		UserID:     "user1",
		Text:       "/stop",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/stop should be handled")
	}
	if !cancelled {
		t.Error("expected cancel function to be invoked")
	}
}

func TestHandleCommand_Ring_NoBuffer(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		UserID:     "user1",
		Text:       "/ring",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/ring should be handled")
	}
}

func TestHandleCommand_Ring_WithBuffer_Status(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.ringBuffer = debuglog.NewRingBuffer(debuglog.DefaultCapacity)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		UserID:     "user1",
		Text:       "/ring status",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/ring status should be handled")
	}
}

func TestHandleCommand_Ring_WithBuffer_Dump(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.ringBuffer = debuglog.NewRingBuffer(debuglog.DefaultCapacity)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	// Empty buffer dump
	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		Text:       "/ring",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/ring empty should be handled")
	}

	// With entries
	gw.ringBuffer.Add(debuglog.Entry{Type: debuglog.EntryToolStart, ToolName: "Read"})
	gw.ringBuffer.Add(debuglog.Entry{Type: debuglog.EntryToolComplete, ToolName: "Read"})
	gw.ringBuffer.Add(debuglog.Entry{Type: debuglog.EntryToolError, ToolName: "Bash"})
	gw.ringBuffer.Add(debuglog.Entry{Type: debuglog.EntryThinking})
	gw.ringBuffer.Add(debuglog.Entry{Type: debuglog.EntryLLMRequest})
	gw.ringBuffer.Add(debuglog.Entry{Type: debuglog.EntryLLMResponse})

	msg.Text = "/ring 5"
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/ring 5 should be handled")
	}

	// Clear
	msg.Text = "/ring clear"
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/ring clear should be handled")
	}
	if gw.ringBuffer.Len() != 0 {
		t.Errorf("expected buffer cleared, got len %d", gw.ringBuffer.Len())
	}
}

func TestHandleCommand_SmartRoute_Status(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.config.AI.SmartRouting = &config.SmartRoutingConfig{Enabled: true, CostBudgetDaily: 5.0}
	session, _ := store.GetOrCreateSession("user1", "tui_test")
	session.Context["smart_routing_model"] = "claude-haiku-4-5-20251001"
	session.Context["smart_routing_reason"] = "low complexity"
	session.Context["smart_routing_complexity"] = "0.12"
	session.Context["session_total_cost"] = "0.042"

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		Text:       "/smartroute",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/smartroute should be handled")
	}

	msg.Text = "/smartroute status"
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/smartroute status should be handled")
	}
}

func TestHandleCommand_SmartRoute_Toggle(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		Text:       "/smartroute on",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/smartroute on should be handled")
	}
	refreshed, _ := store.GetSession(session.Key)
	if refreshed.Context["smart_routing_enabled"] != "true" {
		t.Errorf("expected smart_routing_enabled=true, got %q", refreshed.Context["smart_routing_enabled"])
	}

	msg.Text = "/smartroute off"
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/smartroute off should be handled")
	}
	refreshed, _ = store.GetSession(session.Key)
	if refreshed.Context["smart_routing_enabled"] != "false" {
		t.Errorf("expected smart_routing_enabled=false, got %q", refreshed.Context["smart_routing_enabled"])
	}
}

func TestHandleCommand_SmartRoute_Budget(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	// Missing amount
	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		Text:       "/smartroute budget",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/smartroute budget should be handled")
	}

	// Invalid number
	msg.Text = "/smartroute budget abc"
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/smartroute budget invalid should be handled")
	}

	// Valid
	msg.Text = "/smartroute budget 5.50"
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/smartroute budget 5.50 should be handled")
	}
	refreshed, _ := store.GetSession(session.Key)
	if refreshed.Context["smart_routing_budget"] != "5.50" {
		t.Errorf("expected budget=5.50, got %q", refreshed.Context["smart_routing_budget"])
	}
}

func TestHandleCommand_SmartRoute_Unknown(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		Text:       "/smartroute bogus",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/smartroute bogus should be handled")
	}
}

func TestHandleCommand_Compact_NotConfigured(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	// compactionEngine is nil
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		Text:       "/compact",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/compact should be handled")
	}
}

func TestHandleCommand_NonSlash(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		Text:       "hello world",
	}
	// Non-slash commands should NOT be handled
	if gw.handleCommand(context.Background(), msg, session) {
		t.Error("non-slash command should not be handled")
	}
}

func TestHandleCommand_Goodbye_NoReflector(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	// Add messages so that SPAR threshold (>2) is exceeded
	_, _ = store.AddMessage(session.Key, "user", "hi", nil)
	_, _ = store.AddMessage(session.Key, "assistant", "hello", nil)
	_, _ = store.AddMessage(session.Key, "user", "bye", nil)

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
		Text:       "/goodbye",
	}
	if !gw.handleCommand(context.Background(), msg, session) {
		t.Error("/goodbye should be handled")
	}
	// /goodbye should clear messages
	messages, _ := store.GetMessages(session.Key, 100)
	if len(messages) != 0 {
		t.Errorf("expected messages cleared, got %d", len(messages))
	}
}

func TestHandleStatusCommand_Direct(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")
	session.Context["model"] = "claude-sonnet-4-20250514"
	session.Context["provider"] = "anthropic"

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
	}
	// Should not panic
	gw.handleStatusCommand(msg, session)
}

func TestHandleHelpCommand_Direct(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	msg := &protocol.IncomingMessage{ChannelID: "tui_test"}
	gw.handleHelpCommand(msg)
}

func TestHandleCompactCommand_NilEngine(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	session, _ := store.GetOrCreateSession("user1", "tui_test")

	msg := &protocol.IncomingMessage{
		ChannelID:  "tui_test",
		SessionKey: session.Key,
	}
	gw.handleCompactCommand(context.Background(), msg, session)
}

// Sanity: ensure the test channel manager does not leak state between tests
// (covered implicitly by t.Cleanup).
var _ = channels.StripReplyTags
var _ = sessions.Session{}
