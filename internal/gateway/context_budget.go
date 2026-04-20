package gateway

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"conduit/internal/ai"
	"conduit/internal/sessions"
)

// ContextBudget is a point-in-time snapshot of how much of the model's
// context window has been consumed in a session. It is derived from the
// latest provider usage report: PromptTokens reflects the size of the
// prompt that went into the last inference (which is what actually fills
// the context window), and CompletionTokens reflects the size of the
// response. PercentUsed is computed against the model's declared context
// window.
//
// The budget is intentionally lightweight and non-persistent — it is
// recomputed from values already written to the session's context map on
// every AI response, so no schema change is required.
type ContextBudget struct {
	// PromptTokens is the number of prompt tokens in the most recent AI call.
	// This is the primary gauge of context-window consumption — the next call
	// will start at roughly this size before new user input and tool results.
	PromptTokens int `json:"prompt_tokens"`

	// CompletionTokens is the number of output tokens in the most recent AI call.
	CompletionTokens int `json:"completion_tokens"`

	// TotalTokens is PromptTokens + CompletionTokens for the most recent call.
	TotalTokens int `json:"total_tokens"`

	// SessionPromptTokens is the cumulative prompt tokens across all calls in
	// this session (running total, useful for cost projection / session-wide
	// context growth awareness).
	SessionPromptTokens int `json:"session_prompt_tokens"`

	// SessionCompletionTokens is the cumulative completion tokens across all
	// calls in this session.
	SessionCompletionTokens int `json:"session_completion_tokens"`

	// ModelWindow is the model's declared maximum context window in tokens.
	ModelWindow int `json:"model_window"`

	// Model is the resolved model identifier used for the window lookup.
	Model string `json:"model"`

	// ModelWindowIsDefault is true when the model name didn't match any known
	// entry and the default was returned. Callers can treat this as a hint
	// that the percentage may be inaccurate.
	ModelWindowIsDefault bool `json:"model_window_is_default,omitempty"`

	// PercentUsed is PromptTokens / ModelWindow * 100 (0..100+). It intentionally
	// uses PromptTokens — not total — because that's what's actually in the
	// context window at the start of the next call.
	PercentUsed float64 `json:"percent_used"`

	// RemainingTokens is max(0, ModelWindow - PromptTokens).
	RemainingTokens int `json:"remaining_tokens"`

	// Estimated is true when no provider usage was recorded yet and the gauge
	// was synthesized from the prompt builder's section sizes.
	Estimated bool `json:"estimated,omitempty"`

	// UpdatedAt is the time the gauge was computed.
	UpdatedAt time.Time `json:"updated_at"`
}

// Session context keys used by the context budget. Separated out so the
// router + direct client + gateway all write the same identifiers.
const (
	ctxKeySessionPromptTokensTotal     = "session_prompt_tokens_total"
	ctxKeySessionCompletionTokensTotal = "session_completion_tokens_total"
	ctxKeyContextBudgetUpdatedAt       = "context_budget_updated_at"
)

// GetContextBudgetForSession returns the current context budget for the given session.
// It is safe to call before any AI response has been generated — the returned
// ContextBudget will have zero PromptTokens and PercentUsed, and ModelWindow
// will reflect the session's configured (or default) model.
//
// This method does not mutate state.
func (g *Gateway) GetContextBudgetForSession(sessionKey string) (ContextBudget, error) {
	if g == nil {
		return ContextBudget{}, fmt.Errorf("gateway is nil")
	}
	if g.sessions == nil {
		return ContextBudget{}, fmt.Errorf("session store is not configured")
	}
	session, err := g.sessions.GetSession(sessionKey)
	if err != nil {
		return ContextBudget{}, fmt.Errorf("session %q not found: %w", sessionKey, err)
	}
	return ContextBudgetFromSession(session), nil
}

// GetContextBudget is the GatewayService interface method: returns the context
// budget as a map so tool-layer consumers don't take a direct dependency on
// the gateway.ContextBudget struct. Context is accepted for future
// cancellation support but is not currently used (the lookup is synchronous
// and read-only).
func (g *Gateway) GetContextBudget(_ context.Context, sessionKey string) (map[string]interface{}, error) {
	budget, err := g.GetContextBudgetForSession(sessionKey)
	if err != nil {
		return nil, err
	}
	return budget.ToMap(), nil
}

// ToMap renders the budget as a map suitable for JSON or tool responses.
func (b ContextBudget) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"prompt_tokens":             b.PromptTokens,
		"completion_tokens":         b.CompletionTokens,
		"total_tokens":              b.TotalTokens,
		"session_prompt_tokens":     b.SessionPromptTokens,
		"session_completion_tokens": b.SessionCompletionTokens,
		"model":                     b.Model,
		"model_window":              b.ModelWindow,
		"model_window_is_default":   b.ModelWindowIsDefault,
		"percent_used":              b.PercentUsed,
		"remaining_tokens":          b.RemainingTokens,
		"estimated":                 b.Estimated,
		"updated_at":                b.UpdatedAt,
	}
}

// ContextBudgetFromSession derives a ContextBudget from the persisted session
// context without requiring a gateway instance. Exported so tests and other
// read-only contexts (e.g. future TUI integrations) can compute the same view.
func ContextBudgetFromSession(session *sessions.Session) ContextBudget {
	budget := ContextBudget{
		UpdatedAt: time.Now(),
	}
	if session == nil || session.Context == nil {
		// Synthesize against the default window so percentages are well-defined.
		budget.Model = ""
		budget.ModelWindow = ai.DefaultContextWindow
		budget.ModelWindowIsDefault = true
		return budget
	}

	ctx := session.Context
	budget.PromptTokens = atoiOr(ctx["last_prompt_tokens"], 0)
	budget.CompletionTokens = atoiOr(ctx["last_completion_tokens"], 0)
	budget.TotalTokens = atoiOr(ctx["last_total_tokens"], 0)
	if budget.TotalTokens == 0 && (budget.PromptTokens != 0 || budget.CompletionTokens != 0) {
		budget.TotalTokens = budget.PromptTokens + budget.CompletionTokens
	}

	budget.SessionPromptTokens = atoiOr(ctx[ctxKeySessionPromptTokensTotal], 0)
	budget.SessionCompletionTokens = atoiOr(ctx[ctxKeySessionCompletionTokensTotal], 0)

	// If we don't have cumulative totals yet but have a last reading, seed
	// the cumulative from the last so the gauge is non-zero on first read.
	if budget.SessionPromptTokens == 0 {
		budget.SessionPromptTokens = budget.PromptTokens
	}
	if budget.SessionCompletionTokens == 0 {
		budget.SessionCompletionTokens = budget.CompletionTokens
	}

	budget.Model = ctx["model"]
	budget.ModelWindow, budget.ModelWindowIsDefault = resolveContextWindow(budget.Model)

	if budget.ModelWindow > 0 {
		budget.PercentUsed = float64(budget.PromptTokens) / float64(budget.ModelWindow) * 100.0
		budget.RemainingTokens = budget.ModelWindow - budget.PromptTokens
		if budget.RemainingTokens < 0 {
			budget.RemainingTokens = 0
		}
	}

	if ts := ctx[ctxKeyContextBudgetUpdatedAt]; ts != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			budget.UpdatedAt = parsed
		}
	}

	return budget
}

// resolveContextWindow returns the model window and whether the default was
// used (i.e. the model name matched no known entry).
func resolveContextWindow(model string) (int, bool) {
	if model == "" {
		return ai.DefaultContextWindow, true
	}
	// Exact match
	if size, ok := ai.ContextWindowSizes[model]; ok {
		return size, false
	}
	// Prefix match — same logic as ai.ContextWindowForModel, duplicated here
	// so we can report whether the default was used without a second lookup.
	for prefix, size := range ai.ContextWindowSizes {
		if len(model) >= len(prefix) && model[:len(prefix)] == prefix {
			return size, false
		}
	}
	return ai.DefaultContextWindow, true
}

// recordTokenUsage updates the running cumulative totals plus the
// last_* snapshot and an updated-at timestamp. It is the single place that
// writes budget-related keys so call sites stay consistent. Callers should
// merge the returned map into their own context-batch write.
//
// Safe to call with nil session: returns nil.
func recordTokenUsage(session *sessions.Session, promptTokens, completionTokens, totalTokens int) map[string]string {
	if session == nil {
		return nil
	}
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}
	prevPrompt := 0
	prevCompletion := 0
	if session.Context != nil {
		prevPrompt = atoiOr(session.Context[ctxKeySessionPromptTokensTotal], 0)
		prevCompletion = atoiOr(session.Context[ctxKeySessionCompletionTokensTotal], 0)
	}
	return map[string]string{
		"last_prompt_tokens":               strconv.Itoa(promptTokens),
		"last_completion_tokens":           strconv.Itoa(completionTokens),
		"last_total_tokens":                strconv.Itoa(totalTokens),
		ctxKeySessionPromptTokensTotal:     strconv.Itoa(prevPrompt + promptTokens),
		ctxKeySessionCompletionTokensTotal: strconv.Itoa(prevCompletion + completionTokens),
		ctxKeyContextBudgetUpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// atoiOr parses s as an int, returning fallback on error or empty input.
func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}
