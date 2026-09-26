package ai

// conduit-31jg.15: whole-turn usage accounting.
//
// A turn is many billed round trips: the first call, EmptyGuard retries and
// failover, length auto-continues, and every tool-loop depth (each with its
// own retries/continues). Usage returned for a turn is the SUM of all of
// them, cache token fields included. Context() is the separate gauge for
// context-window occupancy: the prompt size of the turn's LAST round trip.

// Add accumulates o into u. Every counter is summed; ContextTokens tracks
// the most recent round trip that reported a prompt.
func (u *Usage) Add(o Usage) {
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
	total := o.TotalTokens
	if total == 0 {
		total = o.PromptTokens + o.CompletionTokens
	}
	u.TotalTokens += total
	u.CacheCreationInputTokens += o.CacheCreationInputTokens
	u.CacheReadInputTokens += o.CacheReadInputTokens
	if c := o.Context(); c > 0 {
		u.ContextTokens = c
	}
}

// Context returns the context-window occupancy for u: ContextTokens when set
// by Add, else this single round trip's full prompt (uncached input + cache
// writes + cache reads — Anthropic's input_tokens excludes the cached part).
// Use it, not PromptTokens, for compaction and context-budget decisions.
func (u Usage) Context() int {
	if u.ContextTokens > 0 {
		return u.ContextTokens
	}
	return u.PromptTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}
