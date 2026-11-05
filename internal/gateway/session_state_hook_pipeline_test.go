package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/brain"
	"conduit/internal/config"
	"conduit/internal/protocol"
	"conduit/internal/reflection"
	"conduit/internal/statehook"
)

// bd-8sib Path A, end to end through the real TurnRunner: a farewell turn
// gets the state-distillation addendum injected, the reply's state JSON is
// extracted post-turn and atomically written to the configured state file.
func TestTurnRunner_FarewellWritesSessionState(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/memory", 0o755); err != nil {
		t.Fatal(err)
	}
	gw, store, p := newTurnTestGateway(t)
	gw.config = &config.Config{
		Brain:     config.BrainConfig{SessionState: config.SessionStateConfig{Enabled: true}},
		Workspace: config.WorkspaceConfig{ContextDir: dir},
	}
	// Reflection trio armed so IsFarewell/ReflectionPrompt are live, plus
	// the gateway's own hook wiring (integration, not a stub).
	brainDB := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(brainDB, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	rs := reflection.NewStore(b.DB())
	gw.cognition.SessionReflector = reflection.NewSessionReflector(rs)
	gw.cognition.ReflectionStore = rs
	gw.cognition.FarewellDetector = reflection.NewFarewellDetector()
	gw.turnRunner = NewTurnRunner(store, gw.ai, nil, gatewayTurnHooks{gw}, activeTurnRegistry{}, nil, gw.logger)
	gw.setLifecycleCtx(context.Background())

	sess, err := store.GetOrCreateSession("42", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess

	stateJSON, _ := json.Marshal(map[string]any{
		"working_on":  []string{"Acme checkout at 80%"},
		"open_loops":  []string{"Acme deploy"},
		"resume_hint": "Continue Acme E2E suite.",
	})
	p.content = func(n int) string {
		return "Goodnight!\n\n```json\n" + string(stateJSON) + "\n```\nSee you tomorrow."
	}

	msg := &protocol.IncomingMessage{
		ChannelID: "telegram", UserID: "42", Text: "goodbye, thanks for the help",
	}
	gw.handleIncomingMessage(context.Background(), msg)

	// The injected farewell turn must carry the state addendum.
	foundAddendum := false
	for _, req := range p.requests {
		for _, m := range req {
			if contains(m.Content, "SESSION STATE HANDOFF") {
				foundAddendum = true
			}
		}
	}
	if !foundAddendum {
		t.Fatal("farewell turn did not include the state-distillation addendum")
	}

	// Post-turn: the state file must exist, parse and hold all 3 required fields.
	raw, err := os.ReadFile(dir + "/memory/jules-state.json")
	if err != nil {
		t.Fatalf("state file not written after farewell turn: %v", err)
	}
	st, err := statehook.ParseJulesState(raw)
	if err != nil {
		t.Fatalf("state file does not parse: %v", err)
	}
	if st.RequiredFieldCount() != 3 {
		t.Errorf("required fields lost in write: %+v", st)
	}
	if st.WrittenBy != "llama3" && st.WrittenBy == "" {
		t.Errorf("written_by not stamped: %q", st.WrittenBy)
	}
}
