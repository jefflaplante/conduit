package gateway

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/brain"
	"conduit/internal/reflection"
	"conduit/internal/sessions"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestShouldTriggerReflection_NilDetector(t *testing.T) {
	gw := &Gateway{}
	triggered, typ := gw.shouldTriggerReflection("goodbye")
	if triggered {
		t.Errorf("expected no trigger with nil detector, got %v (%v)", triggered, typ)
	}
	if typ != reflection.TriggerNone {
		t.Errorf("expected TriggerNone, got %v", typ)
	}
}

func TestShouldTriggerReflection_Farewell(t *testing.T) {
	gw := &Gateway{farewellDetector: reflection.NewFarewellDetector()}
	triggered, typ := gw.shouldTriggerReflection("goodbye")
	if !triggered {
		t.Error("expected farewell to trigger")
	}
	if typ != reflection.TriggerFarewell {
		t.Errorf("expected TriggerFarewell, got %v", typ)
	}
}

func TestShouldTriggerReflection_Command(t *testing.T) {
	gw := &Gateway{farewellDetector: reflection.NewFarewellDetector()}
	triggered, typ := gw.shouldTriggerReflection("/goodbye")
	if !triggered {
		t.Error("expected /goodbye to trigger")
	}
	if typ != reflection.TriggerCommand {
		t.Errorf("expected TriggerCommand, got %v", typ)
	}
}

func TestShouldTriggerReflection_NoMatch(t *testing.T) {
	gw := &Gateway{farewellDetector: reflection.NewFarewellDetector()}
	triggered, _ := gw.shouldTriggerReflection("hello, how are you today?")
	if triggered {
		t.Error("expected no trigger for greeting")
	}
}

func TestReflectHighConfidencePre_NilReflector(t *testing.T) {
	gw := &Gateway{}
	if got := gw.reflectHighConfidencePre(); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestReflectHighConfidencePre_WithReflector(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(dbPath, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer b.Close()
	store := reflection.NewStore(b.DB())
	refl := reflection.NewSessionReflector(store)

	gw := &Gateway{sessionReflector: refl}
	prompt := gw.reflectHighConfidencePre()
	if prompt == "" {
		t.Error("expected non-empty reflection prompt")
	}
}

func TestReflectHighConfidencePost_NilReflector(t *testing.T) {
	gw := &Gateway{}
	session := &sessions.Session{Key: "s1", MessageCount: 10, CreatedAt: time.Now()}
	// Should not panic
	gw.reflectHighConfidencePost(context.Background(), session)
}

func TestReflectHighConfidencePost_WithReflector(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(dbPath, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer b.Close()
	store := reflection.NewStore(b.DB())
	refl := reflection.NewSessionReflector(store)

	gw := &Gateway{
		sessionReflector: refl,
		reflectionStore:  store,
		logger:           newTestLogger(),
	}
	session := &sessions.Session{
		Key:          "sess-ref",
		MessageCount: 10,
		CreatedAt:    time.Now().Add(-5 * time.Minute),
	}
	gw.reflectHighConfidencePost(context.Background(), session)
}

func TestReflectOnSessionEnd_NilReflector(t *testing.T) {
	gw := &Gateway{}
	// Should return early without panic
	gw.reflectOnSessionEnd(context.Background(), "any-key")
}

func TestReflectOnSessionEnd_NonSubstantive(t *testing.T) {
	// Session with ≤5 messages should be skipped (logged but no summary written)
	dbPath := filepath.Join(t.TempDir(), "gw.db")
	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	session, err := store.GetOrCreateSession("u1", "c1")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}
	// Add 2 messages (below threshold of 5)
	_, _ = store.AddMessage(session.Key, "user", "hi", nil)
	_, _ = store.AddMessage(session.Key, "assistant", "hi", nil)

	brainDB := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(brainDB, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer b.Close()
	rs := reflection.NewStore(b.DB())

	gw := &Gateway{
		sessions:         store,
		logger:           newTestLogger(),
		sessionReflector: reflection.NewSessionReflector(rs),
		reflectionStore:  rs,
	}
	// Should return early because session has <= 5 messages
	gw.reflectOnSessionEnd(context.Background(), session.Key)
}

func TestReflectOnSessionEnd_MissingSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gw.db")
	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	brainDB := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(brainDB, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer b.Close()
	rs := reflection.NewStore(b.DB())

	gw := &Gateway{
		sessions:         store,
		logger:           newTestLogger(),
		sessionReflector: reflection.NewSessionReflector(rs),
		reflectionStore:  rs,
	}
	// No session exists — should not panic
	gw.reflectOnSessionEnd(context.Background(), "no-such-session")
}

func TestReflectOnIdleSessions_NilReflector(t *testing.T) {
	gw := &Gateway{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled immediately
	// Should return early because sessionReflector is nil
	gw.reflectOnIdleSessions(ctx, time.Hour, time.Second)
}

func TestReflectOnIdleSessions_ContextCancel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gw.db")
	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	brainDB := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(brainDB, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer b.Close()
	rs := reflection.NewStore(b.DB())

	gw := &Gateway{
		sessions:         store,
		logger:           newTestLogger(),
		sessionReflector: reflection.NewSessionReflector(rs),
		reflectionStore:  rs,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Run with very short interval; context will cancel quickly
	done := make(chan struct{})
	go func() {
		gw.reflectOnIdleSessions(ctx, time.Hour, 30*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reflectOnIdleSessions did not return after context cancel")
	}
}
