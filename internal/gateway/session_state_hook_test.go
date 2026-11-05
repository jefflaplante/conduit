package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/statehook"
)

// bd-8sib Path A + Path B gateway wiring. All tests run with the hook
// enabled but against a temp state file — never the real workspace one.

func newHookTestState(t *testing.T) (string, *statehook.JulesState) {
	t.Helper()
	body, err := json.MarshalIndent(map[string]any{
		"working_on":  []string{"Acme checkout redesign at 80%"},
		"open_loops":  []string{"Acme staging deploy blocked on review"},
		"resume_hint": "Continue the Acme checkout E2E suite.",
		"written_by":  "test",
		"written_at":  time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(body), nil
}

// newHookGateway builds a Gateway with the session-state hook enabled,
// state file in a temp dir, and returns the configured state-file path.
func newHookGateway(t *testing.T) (*Gateway, *sessions.Store, string) {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "memory", "jules-state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	gw, store, _ := newTestGatewayWithRouter(t)
	gw.config = &config.Config{
		Brain: config.BrainConfig{
			SessionState: config.SessionStateConfig{Enabled: true},
		},
		Workspace: config.WorkspaceConfig{ContextDir: dir},
	}
	return gw, store, statePath
}

// --- stateHookFor ---

func TestStateHookFor_Disabled(t *testing.T) {
	gw, _, _ := newHookGateway(t)
	gw.config.Brain.SessionState.Enabled = false
	if h := gw.stateHookFor("telegram_1_telegram"); h != nil {
		t.Error("expected nil hook when disabled")
	}
}

func TestStateHookFor_ExcludedSessionKeys(t *testing.T) {
	gw, _, _ := newHookGateway(t)
	for _, key := range []string{"cron_123", "heartbeat_123_heartbeat_x", "subagent_123", ""} {
		if h := gw.stateHookFor(key); h != nil {
			t.Errorf("expected nil hook for excluded session %q", key)
		}
	}
}

func TestStateHookFor_ValidSession(t *testing.T) {
	gw, _, _ := newHookGateway(t)
	h := gw.stateHookFor("telegram_1_telegram")
	if h == nil {
		t.Fatal("expected a hook for a main session")
	}
	if h.path == "" {
		t.Error("hook has empty path")
	}
}

// --- Path A: extract + write from a farewell reply ---

func TestMaybeWriteSessionState_FromReply(t *testing.T) {
	gw, store, statePath := newHookGateway(t)
	sess, err := store.GetOrCreateSession("u1", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := newHookTestState(t)
	reply := "Goodnight!\n\n```json\n" + strings.ReplaceAll(body, "\n", "\n") + "\n```"

	gw.maybeWriteSessionState(context.Background(), sess.Key, reply, "farewell")

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	st, err := statehook.ParseJulesState(raw)
	if err != nil {
		t.Fatalf("written state does not parse: %v", err)
	}
	if st.RequiredFieldCount() != 3 {
		t.Errorf("required fields lost: %+v", st)
	}
	if st.WrittenBy == "" || st.WrittenBy == "test" {
		t.Errorf("written_by must be stamped by the hook, got %q", st.WrittenBy)
	}
}

func TestMaybeWriteSessionState_DisabledNoWrite(t *testing.T) {
	gw, store, statePath := newHookGateway(t)
	gw.config.Brain.SessionState.Enabled = false
	sess, _ := store.GetOrCreateSession("u1", "telegram")
	body, _ := newHookTestState(t)

	gw.maybeWriteSessionState(context.Background(), sess.Key, "bye ```json\n"+body+"\n```", "farewell")

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Error("disabled hook must not write the state file")
	}
}

func TestMaybeWriteSessionState_NoJSONIsQuiet(t *testing.T) {
	gw, store, statePath := newHookGateway(t)
	sess, _ := store.GetOrCreateSession("u1", "telegram")

	// No JSON in reply: logged, no error return to caller, no file write.
	gw.maybeWriteSessionState(context.Background(), sess.Key, "goodbye!", "farewell")

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Error("no-JSON reply must not write the state file")
	}
}

func TestMaybeWriteSessionState_GuardRefusesClobber(t *testing.T) {
	gw, store, statePath := newHookGateway(t)
	sess, _ := store.GetOrCreateSession("u1", "telegram")

	// Reply whose JSON has only ONE required field — the empty-clobber
	// guard must refuse, leaving no file behind.
	reply := "```json\n{\"resume_hint\": \"only one field\"}\n```"
	gw.maybeWriteSessionState(context.Background(), sess.Key, reply, "farewell")

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Error("empty-clobber guard failed: file written from 1-field reply")
	}
}

// --- Path B: idle distillation ---

func TestDistillIdleSessionStates(t *testing.T) {
	gw, store, statePath := newHookGateway(t)
	gw.config.Brain.SessionState.IdleDistill = true

	sess, err := store.GetOrCreateSession("u1", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, err := store.AddMessage(sess.Key, role, fmt.Sprintf("Acme message %d", i), nil); err != nil {
			t.Fatal(err)
		}
	}

	body, _ := newHookTestState(t)
	var captured []string
	distilled := make(chan struct{}, 4)
	p := &hookSideCallProvider{
		resp: "```json\n" + body + "\n```",
		onCall: func(req string) {
			captured = append(captured, req)
			distilled <- struct{}{}
		},
	}
	gw.ai.RegisterProvider("testprov", p)

	done := make(chan error, 1)
	go func() {
		done <- gw.distillIdleSessionStates(context.Background(), 0, -time.Hour)
	}()

	select {
	case <-distilled:
	case <-time.After(5 * time.Second):
		t.Fatal("idle distillation never made its side call")
	}
	<-done

	if len(captured) == 0 {
		t.Fatal("no distill call captured")
	}
	if !strings.Contains(captured[0], "Acme message") {
		t.Error("transcript tail missing from distill prompt")
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file not written by Path B: %v", err)
	}
	if st, err := statehook.ParseJulesState(raw); err != nil || st.RequiredFieldCount() != 3 {
		t.Errorf("Path B write invalid: %v %+v", err, st)
	}
}

func TestDistillIdleSessionStates_OffByDefault(t *testing.T) {
	gw, store, _ := newHookGateway(t)
	// Enabled but IdleDistill false (the Stage-1 soak configuration).
	gw.config.Brain.SessionState.IdleDistill = false
	sess, _ := store.GetOrCreateSession("u1", "telegram")
	for i := 0; i < 8; i++ {
		_, _ = store.AddMessage(sess.Key, "user", fmt.Sprintf("m%d", i), nil)
	}
	called := make(chan struct{}, 1)
	gw.ai.RegisterProvider("testprov", &hookSideCallProvider{
		resp:   "```json\n{}\n```",
		onCall: func(string) { called <- struct{}{} },
	})

	done := make(chan error, 1)
	go func() { done <- gw.distillIdleSessionStates(context.Background(), 0, -time.Hour) }()
	<-done

	select {
	case <-called:
		t.Fatal("Path B must not call when idle_distill=false")
	default:
	}
}

func TestDistillIdleSessionStates_SkipsNonSubstantive(t *testing.T) {
	gw, store, _ := newHookGateway(t)
	gw.config.Brain.SessionState.IdleDistill = true
	sess, _ := store.GetOrCreateSession("u1", "telegram")
	for i := 0; i < 3; i++ { // below the >5 substantive threshold
		_, _ = store.AddMessage(sess.Key, "user", fmt.Sprintf("m%d", i), nil)
	}
	called := make(chan struct{}, 1)
	gw.ai.RegisterProvider("testprov", &hookSideCallProvider{
		resp:   "```json\n{}\n```",
		onCall: func(string) { called <- struct{}{} },
	})

	done := make(chan error, 1)
	go func() { done <- gw.distillIdleSessionStates(context.Background(), 0, -time.Hour) }()
	<-done

	select {
	case <-called:
		t.Fatal("Path B must skip non-substantive sessions")
	default:
	}
}

func TestDistillIdleSessionStates_TimestampedFreshnessRule(t *testing.T) {
	// A session whose file was written AFTER the session started must not
	// be rewritten by Path B (fresh write wins; memo §3.1).
	gw, store, _ := newHookGateway(t)
	gw.config.Brain.SessionState.IdleDistill = true
	sess, _ := store.GetOrCreateSession("u1", "telegram")
	for i := 0; i < 8; i++ {
		_, _ = store.AddMessage(sess.Key, "user", fmt.Sprintf("m%d", i), nil)
	}
	called := make(chan struct{}, 1)
	gw.ai.RegisterProvider("testprov", &hookSideCallProvider{
		resp:   "```json\n{}\n```",
		onCall: func(string) { called <- struct{}{} },
	})

	// Seed a state file written "now" (newer than the session's CreatedAt).
	path := gw.sessionStateFilePath()
	os.MkdirAll(filepath.Dir(path), 0o755)
	fresh, _ := json.MarshalIndent(map[string]any{
		"working_on": []string{"fresh"},
		"open_loops": []string{"fresh"},
		"written_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), // future-proof
	}, "", "  ")
	os.WriteFile(path, fresh, 0o600)

	done := make(chan error, 1)
	go func() { done <- gw.distillIdleSessionStates(context.Background(), 0, -time.Hour) }()
	<-done

	select {
	case <-called:
		t.Fatal("Path B rewrote a state file fresher than the session")
	default:
	}
}
