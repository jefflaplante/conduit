package gateway

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/brain"
	"conduit/internal/brain/rem"
	"conduit/internal/config"
	"conduit/internal/reflection"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// Test that handleReflectiveSessionEnd handles the "session too short" path
// (no model reflection fires, only Go-only metrics).
func TestHandleReflectiveSessionEnd_ShortSession(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	gw.logger = newTestLogger()

	// Create a session with <=2 messages so the reflective flow takes the
	// "Go-only metrics" branch.
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")

	// Wire up a reflector + store so the short-session branch does something
	brainPath := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(brainPath, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer b.Close()
	rs := reflection.NewStore(b.DB())
	gw.sessionReflector = reflection.NewSessionReflector(rs)
	gw.reflectionStore = rs

	c := newTestWSClient("c1")
	calls := []string{}
	gw.handleReflectiveSessionEnd(context.Background(), c, sess.Key, func(s string) {
		calls = append(calls, s)
	})
	if len(calls) == 0 {
		t.Error("expected a final response")
	}
	last := calls[len(calls)-1]
	if !strings.Contains(last, "Goodbye") {
		t.Errorf("expected 'Goodbye', got %q", last)
	}
}

// Test the /goodbye WS command routes through handleReflectiveSessionEnd.
func TestHandleWebSocketCommandFromChat_Goodbye(t *testing.T) {
	gw, store, _ := newTestGatewayWithRouter(t)
	gw.logger = newTestLogger()
	sess, _ := store.GetOrCreateSession("u1", "ws_u1")

	c := newTestWSClient("c1")
	gw.handleWebSocketCommandFromChat(context.Background(), c, sess.Key, "/goodbye")
	// Drain at least one message
	out := drainClientMessage(t, c)
	// The /goodbye path calls handleReflectiveSessionEnd which may send
	// multiple messages; we just ensure it doesn't error out.
	_ = out
}

func TestBrainAdapter_Close(t *testing.T) {
	brainPath := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(brainPath, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	a := newBrainAdapter(b)
	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestConvertREMReport(t *testing.T) {
	report := &rem.REMReport{
		Date:   time.Now(),
		DryRun: true,
		Triage: &rem.TriageResult{
			DailyLogScanned: "notes.md",
			WMKeysFound:     10,
			NewFacts:        []string{"a"},
			UpdatedFacts:    []string{"b"},
			StaleCandidates: []string{"c"},
		},
		Consolidation: &rem.ConsolidationResult{
			Promoted:        []string{"k1"},
			Merged:          []rem.MergeRecord{{Kept: "k2", Merged: "k3"}},
			SalienceDecayed: 3,
			SalienceBoosted: 4,
		},
		Pruning: &rem.PruneResult{
			Archived: []rem.ArchiveRecord{{Key: "k4", Reason: "stale"}},
			Orphaned: []string{"k5"},
		},
		Integration: &rem.IntegrationResult{
			RelationshipsCreated: 5,
			Patterns:             []string{"pattern1"},
		},
		Grooming: &rem.GroomResult{
			FilesChecked:       10,
			FilesChanged:       []string{"foo.md"},
			KeysUpdated:        3,
			EntriesMarkedStale: 1,
		},
	}
	out := convertREMReport(report)
	if out == nil {
		t.Fatal("expected non-nil report")
	}
	if !out.DryRun {
		t.Error("expected DryRun=true")
	}
	if out.Triage == nil || out.Consolidation == nil || out.Pruning == nil {
		t.Error("expected all phases to be populated")
	}
	if out.Integration == nil || out.Grooming == nil {
		t.Error("expected integration + grooming populated")
	}
}

func TestConvertREMReport_EmptyPhases(t *testing.T) {
	report := &rem.REMReport{
		Date:   time.Now(),
		DryRun: false,
	}
	out := convertREMReport(report)
	if out == nil {
		t.Fatal("expected non-nil")
	}
	if out.Triage != nil {
		t.Error("expected nil triage when input is nil")
	}
}

func TestNewREMCycleAdapter(t *testing.T) {
	// Only cover the constructor; Run requires an AI router and brain setup
	a := newREMCycleAdapter(nil)
	if a == nil {
		t.Fatal("expected non-nil adapter")
	}
}

// TestSendChatWithID_SessionError exercises the session-error branch where
// GetSession fails and we fall through to GetOrCreateSession.
func TestSendChatWithID_GetSessionPath(t *testing.T) {
	c, store := newTestDirectClient(t)

	// Pre-create session
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")

	// Call SendChatWithID with a known session key, but skip AI since we have no provider.
	// The path under test stops before AI is invoked when text is a slash command.
	if err := c.SendChatWithID(sess.Key, "/help", "req-42"); err != nil {
		t.Fatalf("SendChatWithID: %v", err)
	}
	// Drain the command response
	select {
	case <-c.inbox:
	case <-time.After(time.Second):
		t.Fatal("expected CommandResponseMsg in inbox")
	}
}

func TestSendChatWithID_NewSession(t *testing.T) {
	c, _ := newTestDirectClient(t)
	// Empty session key: SendChatWithID will create a new one (channelID = "tui_<user>")
	// Then hit slash command path to skip AI
	if err := c.SendChatWithID("", "/help", "req-99"); err != nil {
		t.Fatalf("SendChatWithID: %v", err)
	}
	// Drain
	select {
	case <-c.inbox:
	case <-time.After(time.Second):
		t.Fatal("expected message in inbox")
	}
}

// TestWakeSession_Unknown exercises the wakeSession path for a non-existent
// session — should log and return without panic.
func TestWakeSession_Unknown(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()

	// Build a minimal AI router so wakeSession's nil-check passes
	router, err := ai.NewRouter(mkAIConfig(), nil)
	if err != nil {
		t.Fatalf("ai.NewRouter: %v", err)
	}
	gw.ai = router

	// Non-existent key
	gw.wakeSession("no-such-session-xyz")
}

func TestWakeSession_NoMessages(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()

	router, err := ai.NewRouter(mkAIConfig(), nil)
	if err != nil {
		t.Fatalf("ai.NewRouter: %v", err)
	}
	gw.ai = router

	sess, _ := store.GetOrCreateSession("u1", "c1")
	// No messages added — wakeSession should return early after finding none.
	gw.wakeSession(sess.Key)
}

// mkAIConfig returns an AI config with an ollama provider that doesn't
// require API keys.
func mkAIConfig() config.AIConfig {
	return config.AIConfig{
		DefaultProvider: "ollama",
		Providers: []config.ProviderConfig{
			{Name: "ollama", Type: "ollama", Model: "llama3"},
		},
	}
}

// TestInitializeREMCycle_Disabled exercises the disabled-REM branch.
func TestInitializeREMCycle_Disabled(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()
	cfg := &config.Config{}
	cfg.Brain.Enabled = false
	if err := gw.initializeREMCycle(cfg); err != nil {
		t.Errorf("expected no error when brain disabled, got %v", err)
	}
}

func TestInitializeREMCycle_NilCycle(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	gw.logger = newTestLogger()
	cfg := &config.Config{}
	cfg.Brain.Enabled = true
	cfg.Brain.REMEnabled = true
	cfg.Brain.REMSchedule = "0 0 4 * * *"
	// g.remCycle is nil
	if err := gw.initializeREMCycle(cfg); err != nil {
		t.Errorf("expected no error when remCycle nil, got %v", err)
	}
}

// unused imports guard
var _ = types.BrainEntry{}
var _ = sessions.Session{}
