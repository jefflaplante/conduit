package gateway

import (
	"path/filepath"
	"testing"

	"conduit/internal/sessions"
)

// bd-f48o: cron-triggered and wake-path sessions ran full LLM chains but never
// persisted token usage, so SessionStatus's context budget reported 0 tokens
// and observers (heartbeat, SessionStatus tool) concluded the sessions were
// "silently dead" when they had actually completed normally.
//
// Usage is now recorded at the router level through Store.RecordTokenUsage
// (bd-27hs) for every generation path. These tests verify the observable
// contract with a real session store: usage recorded after generation must
// survive a store round-trip and surface in the context budget that
// GetSessionStatus/SessionStatus report.

func newUsageTestStore(t *testing.T) *sessions.Store {
	t.Helper()
	store, err := sessions.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRecordTokenUsage_SurvivesStoreRoundTrip(t *testing.T) {
	store := newUsageTestStore(t)

	session, err := store.GetOrCreateSession("cron", "cron_testjob_1")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	if err := store.RecordTokenUsage(session.Key, 32763, 641, 33404); err != nil {
		t.Fatalf("RecordTokenUsage: %v", err)
	}

	// Re-fetch from the store (as SessionStatus does) and check the budget.
	updated, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	budget := ContextBudgetFromSession(updated)
	if budget.PromptTokens != 32763 {
		t.Errorf("PromptTokens = %d, want 32763", budget.PromptTokens)
	}
	if budget.CompletionTokens != 641 {
		t.Errorf("CompletionTokens = %d, want 641", budget.CompletionTokens)
	}
	if budget.SessionPromptTokens != 32763 {
		t.Errorf("SessionPromptTokens = %d, want 32763", budget.SessionPromptTokens)
	}
	if budget.UpdatedAt.IsZero() {
		t.Error("expected context_budget_updated_at to be set")
	}
}

func TestRecordTokenUsage_CumulativeTotalsAcrossTurns(t *testing.T) {
	store := newUsageTestStore(t)

	session, err := store.GetOrCreateSession("cron", "cron_testjob_2")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	// Turn 1: initial chain. Turn 2: a later wake/session continuation.
	if err := store.RecordTokenUsage(session.Key, 1000, 100, 1100); err != nil {
		t.Fatalf("RecordTokenUsage(1): %v", err)
	}
	if err := store.RecordTokenUsage(session.Key, 2000, 200, 2200); err != nil {
		t.Fatalf("RecordTokenUsage(2): %v", err)
	}

	updated, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("GetSession(final): %v", err)
	}
	ctx := updated.Context
	if got := ctx["session_prompt_tokens_total"]; got != "3000" {
		t.Errorf("session_prompt_tokens_total = %q, want 3000", got)
	}
	if got := ctx["session_completion_tokens_total"]; got != "300" {
		t.Errorf("session_completion_tokens_total = %q, want 300", got)
	}
	if got := ctx["last_prompt_tokens"]; got != "2000" {
		t.Errorf("last_prompt_tokens = %q, want 2000", got)
	}
	if got := ctx["last_completion_tokens"]; got != "200" {
		t.Errorf("last_completion_tokens = %q, want 200", got)
	}
}

func TestRecordTokenUsage_DerivesTotalWhenZero(t *testing.T) {
	store := newUsageTestStore(t)

	session, err := store.GetOrCreateSession("cron", "cron_testjob_3")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}
	if err := store.RecordTokenUsage(session.Key, 100, 20, 0); err != nil {
		t.Fatalf("RecordTokenUsage: %v", err)
	}
	updated, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got := updated.Context["last_total_tokens"]; got != "120" {
		t.Errorf("last_total_tokens = %q, want derived 120", got)
	}
}

func TestRecordTokenUsage_EmptyKeyAndZeroUsageAreNoOps(t *testing.T) {
	store := newUsageTestStore(t)

	if err := store.RecordTokenUsage("", 100, 10, 110); err != nil {
		t.Errorf("empty session key should be a no-op, got %v", err)
	}

	session, err := store.GetOrCreateSession("cron", "cron_testjob_4")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}
	if err := store.RecordTokenUsage(session.Key, 0, 0, 0); err != nil {
		t.Fatalf("RecordTokenUsage(zero): %v", err)
	}
	updated, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got, ok := updated.Context["last_prompt_tokens"]; ok {
		t.Errorf("zero usage must not write last_prompt_tokens, got %q", got)
	}
}
