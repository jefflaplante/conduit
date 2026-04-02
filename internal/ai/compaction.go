package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// CompactionEngine handles automatic context compaction for long sessions.
// When context window usage exceeds a threshold, older messages are summarized
// and replaced with a compact summary to free up context space while preserving
// important context.
type CompactionEngine struct {
	router       *Router
	sessionStore *sessions.Store
	config       config.CompactionConfig
}

// NewCompactionEngine creates a new compaction engine with the given dependencies.
func NewCompactionEngine(router *Router, store *sessions.Store, cfg config.CompactionConfig) *CompactionEngine {
	return &CompactionEngine{
		router:       router,
		sessionStore: store,
		config:       cfg,
	}
}

// ShouldCompact determines whether a session should be compacted based on
// the current prompt token count and the model's context window.
func (ce *CompactionEngine) ShouldCompact(promptTokens int, model string) bool {
	if !ce.config.Enabled {
		return false
	}

	contextWindow := ContextWindowForModel(model)
	if contextWindow == 0 {
		return false
	}

	usage := float64(promptTokens) / float64(contextWindow)
	threshold := ce.config.Threshold
	if threshold == 0 {
		threshold = 0.70
	}

	return usage >= threshold
}

// CompactionResult contains information about a completed compaction operation.
type CompactionResult struct {
	// OriginalCount is the total number of messages before compaction.
	OriginalCount int `json:"original_count"`

	// SummarizedCount is the number of messages that were summarized.
	SummarizedCount int `json:"summarized_count"`

	// KeptCount is the number of recent messages preserved without summarization.
	KeptCount int `json:"kept_count"`

	// SummaryTokens is an estimate of the token count in the generated summary.
	SummaryTokens int `json:"summary_tokens"`
}

// Compact performs context compaction on a session by summarizing older messages.
// It preserves the most recent messages (configured via RecentMessagesToKeep)
// and replaces older messages with a concise summary.
//
// Returns nil (no result) if there are not enough messages to compact.
func (ce *CompactionEngine) Compact(ctx context.Context, session *sessions.Session) (*CompactionResult, error) {
	// 1. Get all messages
	messages, err := ce.sessionStore.GetMessages(session.Key, 10000)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	recentCount := ce.config.RecentMessagesToKeep
	if recentCount == 0 {
		recentCount = 10
	}

	// Not enough messages to warrant compaction
	if len(messages) <= recentCount {
		return nil, nil
	}

	// 2. Split into history (to summarize) and recent (to keep)
	historyMessages := messages[:len(messages)-recentCount]
	recentMessages := messages[len(messages)-recentCount:]

	// 3. Build summarization prompt
	var historyText strings.Builder
	for _, msg := range historyMessages {
		historyText.WriteString(fmt.Sprintf("%s: %s\n\n", msg.Role, msg.Content))
	}

	summarizePrompt := fmt.Sprintf(`Summarize the following conversation concisely. Preserve:
- Key decisions made
- Active tasks or goals
- User preferences mentioned
- Important technical context
- Any unresolved questions

Keep the summary under 500 words.

Conversation:
%s`, historyText.String())

	// 4. Call router with cheap model for summarization
	model := ce.config.Model
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}

	// Create a temporary session for summarization to avoid polluting the original
	tempSession := &sessions.Session{
		Key:       session.Key + "_compact_temp",
		UserID:    session.UserID,
		ChannelID: session.ChannelID,
		Context:   make(map[string]string),
	}
	tempSession.Context["model"] = model

	resp, err := ce.router.GenerateResponse(ctx, tempSession, summarizePrompt, "")
	if err != nil {
		return nil, fmt.Errorf("summarization failed: %w", err)
	}

	summary := resp.Content

	// 5. Clear session and rebuild with summary + recent messages
	if err := ce.sessionStore.ClearSessionMessages(session.Key); err != nil {
		return nil, fmt.Errorf("clear messages: %w", err)
	}

	// Add summary as assistant message with clear marker
	summaryContent := fmt.Sprintf("[Context Summary from %d previous messages]\n\n%s", len(historyMessages), summary)
	if _, err := ce.sessionStore.AddMessage(session.Key, "assistant", summaryContent, nil); err != nil {
		return nil, fmt.Errorf("add summary: %w", err)
	}

	// Re-add recent messages
	for _, msg := range recentMessages {
		metadata := msg.Metadata
		if metadata == nil {
			metadata = make(map[string]string)
		}
		if _, err := ce.sessionStore.AddMessage(session.Key, msg.Role, msg.Content, metadata); err != nil {
			return nil, fmt.Errorf("re-add message: %w", err)
		}
	}

	// 6. Store compaction metadata in session context
	_ = ce.sessionStore.SetSessionContext(session.Key, "compact_timestamp", time.Now().Format(time.RFC3339))
	_ = ce.sessionStore.SetSessionContext(session.Key, "compact_original_count", fmt.Sprintf("%d", len(messages)))

	log.Printf("[Compaction] Session %s: compacted %d messages to summary + %d recent", session.Key, len(historyMessages), len(recentMessages))

	return &CompactionResult{
		OriginalCount:   len(messages),
		SummarizedCount: len(historyMessages),
		KeptCount:       len(recentMessages),
		SummaryTokens:   len(summary) / 4, // rough estimate: 4 chars per token
	}, nil
}
