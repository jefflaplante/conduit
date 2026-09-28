package sessions

import (
	"strconv"
	"time"
)

// Context keys written by RecordTokenUsage. These power SessionStatus's
// context-budget gauge (last_* snapshot keys) and cumulative session totals.
// Declared here so the sessions package is the single source of truth for the
// key names; the gateway package mirrors them via exported constants.
const (
	CtxKeyLastPromptTokens         = "last_prompt_tokens"
	CtxKeyLastCompletionTokens     = "last_completion_tokens"
	CtxKeyLastTotalTokens          = "last_total_tokens"
	CtxKeySessionPromptTokensTotal = "session_prompt_tokens_total"
	CtxKeySessionCompletionTokens  = "session_completion_tokens_total"
	CtxKeyContextBudgetUpdatedAt   = "context_budget_updated_at"
)

// RecordTokenUsage persists a token-usage snapshot to the session's context:
// the last_* keys reflect the most recent generation, the session_*_tokens_total
// keys accumulate across generations, and context_budget_updated_at is an
// RFC3339 timestamp. It reads the current cumulative totals from the store
// (single round trip) and merges everything in one batched context write.
//
// This is the router-level accounting path (bd-27hs): every generation entry
// point records through here, so usage is uniform across channel, direct,
// cron, wake, sub-agent, WS and HTTP callers. Zero-token usage is a no-op.
// Best-effort: errors are returned for the caller to log/ignore.
func (s *Store) RecordTokenUsage(sessionKey string, promptTokens, completionTokens, totalTokens int) error {
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}
	return s.RecordTurnUsage(sessionKey, TurnUsage{
		ContextTokens:    promptTokens,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	})
}

// Cache token context keys written by RecordTurnUsage (conduit-31jg.15).
const (
	CtxKeyLastCacheCreationTokens     = "last_cache_creation_tokens"
	CtxKeyLastCacheReadTokens         = "last_cache_read_tokens"
	CtxKeySessionCacheCreationTotal   = "session_cache_creation_tokens_total"
	CtxKeySessionCacheReadTokensTotal = "session_cache_read_tokens_total"
)

// TurnUsage is one turn's token accounting as recorded by RecordTurnUsage.
// ContextTokens is the context-window occupancy (the prompt size of the
// turn's last round trip, cached input included — ai.Usage.Context()); the
// other fields are whole-turn sums across every billed round trip.
type TurnUsage struct {
	ContextTokens            int
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// RecordTurnUsage persists a turn's usage (conduit-31jg.15). The
// last_prompt_tokens / last_total_tokens keys drive the context-budget gauge
// and /context, so they hold context occupancy (ContextTokens), not the
// turn-wide prompt sum, which overstates occupancy on multi-round tool
// turns. last_total_tokens is ContextTokens + the turn's completion tokens.
// The session_*_total keys accumulate the billed sums, and the cache token
// fields are recorded alongside (last_* = this turn's sums).
func (s *Store) RecordTurnUsage(sessionKey string, u TurnUsage) error {
	if sessionKey == "" {
		return nil
	}
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.ContextTokens == 0 &&
		u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 {
		return nil
	}
	if u.ContextTokens == 0 {
		u.ContextTokens = u.PromptTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	}

	// Read current context to pick up the running cumulative totals.
	session, err := s.GetSession(sessionKey)
	if err != nil {
		return err
	}
	prevPrompt := atoiContext(session.Context, CtxKeySessionPromptTokensTotal)
	prevCompletion := atoiContext(session.Context, CtxKeySessionCompletionTokens)
	prevCacheCreate := atoiContext(session.Context, CtxKeySessionCacheCreationTotal)
	prevCacheRead := atoiContext(session.Context, CtxKeySessionCacheReadTokensTotal)

	return s.SetSessionContextBatch(sessionKey, map[string]string{
		CtxKeyLastPromptTokens:            strconv.Itoa(u.ContextTokens),
		CtxKeyLastCompletionTokens:        strconv.Itoa(u.CompletionTokens),
		CtxKeyLastTotalTokens:             strconv.Itoa(u.ContextTokens + u.CompletionTokens),
		CtxKeyLastCacheCreationTokens:     strconv.Itoa(u.CacheCreationInputTokens),
		CtxKeyLastCacheReadTokens:         strconv.Itoa(u.CacheReadInputTokens),
		CtxKeySessionPromptTokensTotal:    strconv.Itoa(prevPrompt + u.PromptTokens),
		CtxKeySessionCompletionTokens:     strconv.Itoa(prevCompletion + u.CompletionTokens),
		CtxKeySessionCacheCreationTotal:   strconv.Itoa(prevCacheCreate + u.CacheCreationInputTokens),
		CtxKeySessionCacheReadTokensTotal: strconv.Itoa(prevCacheRead + u.CacheReadInputTokens),
		CtxKeyContextBudgetUpdatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// atoiContext parses a context value as an int, returning 0 on absence/error.
func atoiContext(ctx map[string]string, key string) int {
	v, err := strconv.Atoi(ctx[key])
	if err != nil {
		return 0
	}
	return v
}
