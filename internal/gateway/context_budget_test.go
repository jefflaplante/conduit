package gateway

import (
	"context"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/sessions"
)

func TestContextBudgetFromSession_NilSafe(t *testing.T) {
	b := ContextBudgetFromSession(nil)
	if b.ModelWindow != ai.DefaultContextWindow {
		t.Errorf("nil session should fall back to DefaultContextWindow, got %d", b.ModelWindow)
	}
	if !b.ModelWindowIsDefault {
		t.Error("nil session should mark ModelWindowIsDefault=true")
	}
	if b.PromptTokens != 0 || b.PercentUsed != 0 {
		t.Errorf("nil session should produce zero-valued tokens, got prompt=%d pct=%.2f",
			b.PromptTokens, b.PercentUsed)
	}
}

func TestContextBudgetFromSession_NoUsageYet(t *testing.T) {
	session := &sessions.Session{
		Key: "s1",
		Context: map[string]string{
			"model": "claude-sonnet-4-20250514",
		},
	}
	b := ContextBudgetFromSession(session)
	if b.PromptTokens != 0 {
		t.Errorf("expected 0 prompt tokens, got %d", b.PromptTokens)
	}
	if b.ModelWindow != 200000 {
		t.Errorf("expected 200k window for sonnet-4, got %d", b.ModelWindow)
	}
	if b.ModelWindowIsDefault {
		t.Error("sonnet-4 should be a known prefix, not default")
	}
	if b.PercentUsed != 0 {
		t.Errorf("expected 0%% used, got %.2f", b.PercentUsed)
	}
	if b.RemainingTokens != 200000 {
		t.Errorf("expected 200k remaining, got %d", b.RemainingTokens)
	}
}

func TestContextBudgetFromSession_WithUsage(t *testing.T) {
	session := &sessions.Session{
		Key: "s2",
		Context: map[string]string{
			"model":                  "claude-sonnet-4-20250514",
			"last_prompt_tokens":     "50000",
			"last_completion_tokens": "500",
			"last_total_tokens":      "50500",
		},
	}
	b := ContextBudgetFromSession(session)
	if b.PromptTokens != 50000 {
		t.Errorf("expected 50000 prompt tokens, got %d", b.PromptTokens)
	}
	if b.CompletionTokens != 500 {
		t.Errorf("expected 500 completion tokens, got %d", b.CompletionTokens)
	}
	if b.TotalTokens != 50500 {
		t.Errorf("expected 50500 total tokens, got %d", b.TotalTokens)
	}
	if b.ModelWindow != 200000 {
		t.Errorf("expected 200000 window, got %d", b.ModelWindow)
	}
	// 50000 / 200000 = 25.0%
	if b.PercentUsed < 24.99 || b.PercentUsed > 25.01 {
		t.Errorf("expected ~25%% used, got %.2f", b.PercentUsed)
	}
	if b.RemainingTokens != 150000 {
		t.Errorf("expected 150000 remaining, got %d", b.RemainingTokens)
	}
	// Cumulative totals default to the last reading if unset
	if b.SessionPromptTokens != 50000 {
		t.Errorf("expected session prompt total to seed from last, got %d", b.SessionPromptTokens)
	}
}

func TestContextBudgetFromSession_UnknownModelUsesDefault(t *testing.T) {
	session := &sessions.Session{
		Key: "s3",
		Context: map[string]string{
			"model":              "some-future-model-name",
			"last_prompt_tokens": "100",
		},
	}
	b := ContextBudgetFromSession(session)
	if !b.ModelWindowIsDefault {
		t.Error("unknown model should mark default=true")
	}
	if b.ModelWindow != ai.DefaultContextWindow {
		t.Errorf("unknown model should use default window, got %d", b.ModelWindow)
	}
}

func TestContextBudgetFromSession_ComputesTotalFromParts(t *testing.T) {
	// last_total_tokens missing — should derive from prompt + completion
	session := &sessions.Session{
		Key: "s4",
		Context: map[string]string{
			"model":                  "claude-opus-4-6",
			"last_prompt_tokens":     "1000",
			"last_completion_tokens": "200",
		},
	}
	b := ContextBudgetFromSession(session)
	if b.TotalTokens != 1200 {
		t.Errorf("expected derived total of 1200, got %d", b.TotalTokens)
	}
}

func TestContextBudgetFromSession_RemainingNeverNegative(t *testing.T) {
	// Over-budget scenario (prompt > window). Remaining should clamp to 0.
	session := &sessions.Session{
		Key: "s5",
		Context: map[string]string{
			"model":              "gpt-4", // 8192 window
			"last_prompt_tokens": "10000", // exceeds window
		},
	}
	b := ContextBudgetFromSession(session)
	if b.RemainingTokens != 0 {
		t.Errorf("expected remaining to clamp at 0, got %d", b.RemainingTokens)
	}
	if b.PercentUsed < 100 {
		t.Errorf("expected percent >= 100 over budget, got %.2f", b.PercentUsed)
	}
}

func TestResolveContextWindow(t *testing.T) {
	tests := []struct {
		model         string
		wantWindow    int
		wantIsDefault bool
	}{
		{"", ai.DefaultContextWindow, true},
		{"claude-sonnet-4-20250514", 200000, false},
		{"claude-opus-4-6", 200000, false},
		{"claude-haiku-4-5-20251001", 200000, false},
		{"gpt-4o", 128000, false},
		{"gpt-4", 8192, false},
		{"llama3", 8192, false},
		{"completely-unknown-model-xyz", ai.DefaultContextWindow, true},
	}
	for _, tc := range tests {
		window, isDefault := resolveContextWindow(tc.model)
		if window != tc.wantWindow {
			t.Errorf("resolveContextWindow(%q) window = %d, want %d", tc.model, window, tc.wantWindow)
		}
		if isDefault != tc.wantIsDefault {
			t.Errorf("resolveContextWindow(%q) isDefault = %v, want %v", tc.model, isDefault, tc.wantIsDefault)
		}
	}
}

func TestRecordTokenUsage_AccumulatesRunningTotals(t *testing.T) {
	session := &sessions.Session{
		Key: "s6",
		Context: map[string]string{
			ctxKeySessionPromptTokensTotal:     "100",
			ctxKeySessionCompletionTokensTotal: "50",
		},
	}
	batch := recordTokenUsage(session, 25, 10, 35)
	if batch == nil {
		t.Fatal("expected non-nil batch")
	}
	if batch["last_prompt_tokens"] != "25" {
		t.Errorf("expected last_prompt_tokens=25, got %s", batch["last_prompt_tokens"])
	}
	if batch["last_completion_tokens"] != "10" {
		t.Errorf("expected last_completion_tokens=10, got %s", batch["last_completion_tokens"])
	}
	if batch["last_total_tokens"] != "35" {
		t.Errorf("expected last_total_tokens=35, got %s", batch["last_total_tokens"])
	}
	if batch[ctxKeySessionPromptTokensTotal] != "125" {
		t.Errorf("expected cumulative prompt=125, got %s", batch[ctxKeySessionPromptTokensTotal])
	}
	if batch[ctxKeySessionCompletionTokensTotal] != "60" {
		t.Errorf("expected cumulative completion=60, got %s", batch[ctxKeySessionCompletionTokensTotal])
	}
	if batch[ctxKeyContextBudgetUpdatedAt] == "" {
		t.Error("expected updated_at timestamp to be set")
	}
}

func TestRecordTokenUsage_DerivesTotalWhenZero(t *testing.T) {
	session := &sessions.Session{Key: "s7", Context: map[string]string{}}
	batch := recordTokenUsage(session, 100, 20, 0)
	if batch["last_total_tokens"] != "120" {
		t.Errorf("expected derived total=120, got %s", batch["last_total_tokens"])
	}
}

func TestRecordTokenUsage_NilSession(t *testing.T) {
	if batch := recordTokenUsage(nil, 1, 2, 3); batch != nil {
		t.Errorf("expected nil batch for nil session, got %v", batch)
	}
}

func TestGetContextBudget_MethodIntegration(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	sess, _ := store.GetOrCreateSession("u1", "c1")
	_ = store.SetSessionContextBatch(sess.Key, map[string]string{
		"model":                  "claude-sonnet-4-20250514",
		"last_prompt_tokens":     "10000",
		"last_completion_tokens": "100",
		"last_total_tokens":      "10100",
	})

	// Struct-returning variant.
	b, err := gw.GetContextBudgetForSession(sess.Key)
	if err != nil {
		t.Fatalf("GetContextBudgetForSession: %v", err)
	}
	if b.PromptTokens != 10000 {
		t.Errorf("expected prompt=10000, got %d", b.PromptTokens)
	}
	if b.ModelWindow != 200000 {
		t.Errorf("expected window=200000, got %d", b.ModelWindow)
	}

	// GatewayService map-returning variant.
	m, err := gw.GetContextBudget(context.Background(), sess.Key)
	if err != nil {
		t.Fatalf("GetContextBudget: %v", err)
	}
	if pt, _ := m["prompt_tokens"].(int); pt != 10000 {
		t.Errorf("map prompt_tokens = %v, want 10000", m["prompt_tokens"])
	}
	if win, _ := m["model_window"].(int); win != 200000 {
		t.Errorf("map model_window = %v, want 200000", m["model_window"])
	}
}

func TestGetContextBudget_NotFound(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	_, err := gw.GetContextBudgetForSession("no-such-session")
	if err == nil {
		t.Error("expected error for missing session")
	}
}

func TestGetContextBudget_NilGateway(t *testing.T) {
	var gw *Gateway
	_, err := gw.GetContextBudgetForSession("anything")
	if err == nil {
		t.Error("expected error for nil gateway")
	}
}

func TestGetContextBudget_SessionStoreNil(t *testing.T) {
	gw := &Gateway{}
	_, err := gw.GetContextBudgetForSession("anything")
	if err == nil {
		t.Error("expected error when session store not configured")
	}
}

func TestContextBudget_ToMap_RoundtripsKeys(t *testing.T) {
	b := ContextBudget{
		PromptTokens:         1234,
		CompletionTokens:     56,
		TotalTokens:          1290,
		ModelWindow:          200000,
		Model:                "claude-sonnet-4",
		ModelWindowIsDefault: false,
		PercentUsed:          0.617,
		RemainingTokens:      198766,
	}
	m := b.ToMap()
	expected := []string{
		"prompt_tokens", "completion_tokens", "total_tokens",
		"session_prompt_tokens", "session_completion_tokens",
		"model", "model_window", "model_window_is_default",
		"percent_used", "remaining_tokens", "estimated", "updated_at",
	}
	for _, k := range expected {
		if _, ok := m[k]; !ok {
			t.Errorf("ToMap missing key %q", k)
		}
	}
}

func TestGetSessionStatus_IncludesBudget(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	sess, _ := store.GetOrCreateSession("u2", "c2")
	_ = store.SetSessionContextBatch(sess.Key, map[string]string{
		"model":              "claude-sonnet-4-20250514",
		"last_prompt_tokens": "500",
	})
	status, err := gw.GetSessionStatus(context.Background(), sess.Key)
	if err != nil {
		t.Fatalf("GetSessionStatus: %v", err)
	}
	budget, ok := status["context_budget"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected context_budget in status, got %T", status["context_budget"])
	}
	if pt, _ := budget["prompt_tokens"].(int); pt != 500 {
		t.Errorf("budget prompt_tokens = %v, want 500", budget["prompt_tokens"])
	}
}
