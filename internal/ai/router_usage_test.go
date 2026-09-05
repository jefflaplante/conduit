package ai

import (
	"context"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// bd-27hs: Router-level token usage recording.
//
// Previously usage recording was per-path glue in the gateway package — only
// the channel-message and direct-client paths wrote last_prompt_tokens /
// session_prompt_tokens_total. Cron, wake, sub-agent, WS-chat, HTTP and
// heartbeat paths either never recorded usage or recorded drifted variants
// (ws_chat wrote last_* but not the cumulative session_*_tokens_total keys).
// These tests verify the new contract: Router.GenerateResponseWithTools /
// GenerateResponseStreaming persist usage to the session store themselves, so
// every generation path records uniformly regardless of caller.

func newUsageStore(t *testing.T) *sessions.Store {
	t.Helper()
	store, err := sessions.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newUsageTestRouter(t *testing.T, store *sessions.Store) (*Router, *MockProvider) {
	t.Helper()
	cfg := config.AIConfig{DefaultProvider: "mock", Providers: []config.ProviderConfig{}}
	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	mock := NewMockProvider("mock")
	router.RegisterProvider("mock", mock)
	if store != nil {
		router.SetSessionStore(store)
	}
	return router, mock
}

func newStoredSession(t *testing.T, store *sessions.Store, key string) *sessions.Session {
	t.Helper()
	session, err := store.GetOrCreateSession("test-user", key)
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}
	return session
}

func mustGetSession(t *testing.T, store *sessions.Store, key string) *sessions.Session {
	t.Helper()
	s, err := store.GetSession(key)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	return s
}

// TestRouter_GenerateResponseWithTools_RecordsUsageToStore is the core
// contract: a successful generation must persist last_* and cumulative
// session_*_tokens_total keys to the session store, retrievable after a fresh
// GetSession — no caller glue required.
func TestRouter_GenerateResponseWithTools_RecordsUsageToStore(t *testing.T) {
	store := newUsageStore(t)
	router, mock := newUsageTestRouter(t, store)
	session := newStoredSession(t, store, "usage_nonstream_1")

	mock.AddResponse("hello world", nil) // no tool calls → SimpleConversationResponse

	resp, err := router.GenerateResponseWithTools(context.Background(), session, "test", "mock", "")
	if err != nil {
		t.Fatalf("GenerateResponseWithTools: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	fetched := mustGetSession(t, store, session.Key)
	if got := fetched.Context["last_prompt_tokens"]; got != "10" {
		t.Errorf("last_prompt_tokens = %q, want \"10\" (router must record usage)", got)
	}
	if got := fetched.Context["last_completion_tokens"]; got != "5" {
		t.Errorf("last_completion_tokens = %q, want \"5\"", got)
	}
	if got := fetched.Context["session_prompt_tokens_total"]; got != "10" {
		t.Errorf("session_prompt_tokens_total = %q, want \"10\" (cumulative key — ws_chat drift bug)", got)
	}
	if got := fetched.Context["session_completion_tokens_total"]; got != "5" {
		t.Errorf("session_completion_tokens_total = %q, want \"5\"", got)
	}
	if got := fetched.Context["context_budget_updated_at"]; got == "" {
		t.Error("context_budget_updated_at should be set")
	}
}

// TestRouter_GenerateResponseStreaming_RecordsUsageToStore covers the
// streaming entry point (channel path uses it heavily).
func TestRouter_GenerateResponseStreaming_RecordsUsageToStore(t *testing.T) {
	store := newUsageStore(t)
	router, mock := newUsageTestRouter(t, store)
	session := newStoredSession(t, store, "usage_stream_1")

	mock.AddResponse("streamed answer", nil)

	resp, err := router.GenerateResponseStreaming(context.Background(), session, "test", "mock", "", func(s string, b bool) {})
	if err != nil {
		t.Fatalf("GenerateResponseStreaming: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	fetched := mustGetSession(t, store, session.Key)
	if got := fetched.Context["last_prompt_tokens"]; got != "10" {
		t.Errorf("last_prompt_tokens = %q, want \"10\"", got)
	}
	if got := fetched.Context["session_prompt_tokens_total"]; got != "10" {
		t.Errorf("session_prompt_tokens_total = %q, want \"10\"", got)
	}
}

// TestRouter_UsageRecording_CumulativeAcrossTurns verifies cumulative totals
// accumulate across successive turns (fresh GetSession per turn, as the wake
// path would see).
func TestRouter_UsageRecording_CumulativeAcrossTurns(t *testing.T) {
	store := newUsageStore(t)
	router, mock := newUsageTestRouter(t, store)
	session := newStoredSession(t, store, "usage_cumulative_1")

	mock.AddResponse("turn one", nil)
	mock.AddResponse("turn two", nil)

	for i := 0; i < 2; i++ {
		s := mustGetSession(t, store, session.Key)
		if _, err := router.GenerateResponseWithTools(context.Background(), s, "test", "mock", ""); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}

	fetched := mustGetSession(t, store, session.Key)
	if got := fetched.Context["session_prompt_tokens_total"]; got != "20" {
		t.Errorf("session_prompt_tokens_total = %q, want \"20\" (2 turns × 10)", got)
	}
	if got := fetched.Context["last_prompt_tokens"]; got != "10" {
		t.Errorf("last_prompt_tokens = %q, want \"10\" (latest turn)", got)
	}
}

// TestRouter_UsageRecording_NoStoreIsNoop verifies nil sessionStore is safe —
// generation still succeeds, nothing recorded.
func TestRouter_UsageRecording_NoStoreIsNoop(t *testing.T) {
	router, mock := newUsageTestRouter(t, nil)
	session := &sessions.Session{Key: "no_store_session"}

	mock.AddResponse("no store response", nil)
	resp, err := router.GenerateResponseWithTools(context.Background(), session, "test", "mock", "")
	if err != nil {
		t.Fatalf("GenerateResponseWithTools: %v", err)
	}
	if resp.GetContent() != "no store response" {
		t.Errorf("content = %q", resp.GetContent())
	}
}

// TestRouter_UsageRecording_ZeroUsageSkipped: a usage block with all-zero
// token fields should not write a misleading "0" snapshot.
func TestRouter_UsageRecording_ZeroUsageSkipped(t *testing.T) {
	store := newUsageStore(t)
	router, mock := newUsageTestRouter(t, store)
	session := newStoredSession(t, store, "usage_zero_1")

	mock.SetResponses([]MockResponse{{Content: "empty usage", Usage: Usage{}}})

	if _, err := router.GenerateResponseWithTools(context.Background(), session, "test", "mock", ""); err != nil {
		t.Fatalf("GenerateResponseWithTools: %v", err)
	}

	fetched := mustGetSession(t, store, session.Key)
	if got := fetched.Context["last_prompt_tokens"]; got == "0" {
		t.Errorf("zero-token usage should not write last_prompt_tokens=\"0\", context=%v", fetched.Context)
	}
}

// TestRouter_UsageRecording_WithToolFlow verifies usage records when the
// tool-execution engine participates (MockExecutionEngine returns
// initialResp.Usage).
func TestRouter_UsageRecording_WithToolFlow(t *testing.T) {
	store := newUsageStore(t)
	router, mock := newUsageTestRouter(t, store)
	session := newStoredSession(t, store, "usage_toolflow_1")

	mock.AddResponse("I'll help", []ToolCall{{ID: "tc1", Name: "test_tool", Args: map[string]interface{}{}}})

	router, err := NewRouterWithExecution(config.AIConfig{DefaultProvider: "mock"}, nil, &MockExecutionEngine{responseContent: "tool done"})
	if err != nil {
		t.Fatalf("NewRouterWithExecution: %v", err)
	}
	router.RegisterProvider("mock", mock)
	router.SetSessionStore(store)

	resp, err := router.GenerateResponseWithTools(context.Background(), session, "test", "mock", "")
	if err != nil {
		t.Fatalf("GenerateResponseWithTools: %v", err)
	}
	if resp.GetContent() != "tool done" {
		t.Errorf("content = %q, want \"tool done\"", resp.GetContent())
	}

	fetched := mustGetSession(t, store, session.Key)
	if got := fetched.Context["last_prompt_tokens"]; got != "10" {
		t.Errorf("last_prompt_tokens = %q, want \"10\" (tool flow must record usage)", got)
	}
	if got := fetched.Context["session_prompt_tokens_total"]; got != "10" {
		t.Errorf("session_prompt_tokens_total = %q, want \"10\"", got)
	}
}
