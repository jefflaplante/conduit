package gateway

import (
	"path/filepath"
	"strconv"
	"testing"

	"conduit/internal/sessions"
)

// bd-f48o: cron-triggered and wake-path sessions ran full LLM chains but never
// persisted token usage, so SessionStatus's context budget reported 0 tokens
// and observers (heartbeat, SessionStatus tool) concluded the sessions were
// "silently dead" when they had actually completed normally.
//
// The fix routes those paths through recordTokenUsage + SetSessionContextBatch
// — the same accounting the channel-message path has used all along. This test
// verifies the observable contract with a real session store: usage recorded
// after generation must survive a store round-trip and surface in the context
// budget that GetSessionStatus/SessionStatus report.

func TestRecordTokenUsage_SurvivesStoreRoundTrip(t *testing.T) {
	store, err := sessions.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	session, err := store.GetOrCreateSession("cron", "cron_testjob_1")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	// Simulate the post-generation accounting the fixed paths perform.
	batch := recordTokenUsage(session, 32763, 641, 33404)
	if len(batch) == 0 {
		t.Fatal("recordTokenUsage returned empty batch")
	}
	if err := store.SetSessionContextBatch(session.Key, batch); err != nil {
		t.Fatalf("SetSessionContextBatch: %v", err)
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
		t.Errorf("SessionPromptTokens = %d, want 32763 (cumulative seeded from last)", budget.SessionPromptTokens)
	}
}

func TestRecordTokenUsage_CumulativeTotalsAcrossTurns(t *testing.T) {
	store, err := sessions.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	session, err := store.GetOrCreateSession("cron", "cron_testjob_2")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	// Turn 1: initial chain.
	b1 := recordTokenUsage(session, 1000, 100, 1100)
	if err := store.SetSessionContextBatch(session.Key, b1); err != nil {
		t.Fatalf("SetSessionContextBatch(1): %v", err)
	}

	// Turn 2: a later wake/session continuation. recordTokenUsage reads the
	// session object's prior cumulative total — the fixed paths must re-fetch
	// the session (or the totals will reset), mirroring the channel path which
	// operates on a fresh session per message.
	s2, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("GetSession(2): %v", err)
	}
	b2 := recordTokenUsage(s2, 2000, 200, 2200)
	if err := store.SetSessionContextBatch(session.Key, b2); err != nil {
		t.Fatalf("SetSessionContextBatch(2): %v", err)
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
	if got := ctx["last_prompt_tokens"]; got != strconv.Itoa(2000) {
		t.Errorf("last_prompt_tokens = %q, want 2000", got)
	}
}

func TestRecordTokenUsage_NilSessionSafe(t *testing.T) {
	if batch := recordTokenUsage(nil, 100, 10, 110); batch != nil {
		t.Errorf("nil session should return nil batch, got %v", batch)
	}
}
