package ai

import (
	"context"
	"fmt"
	"log"
	"strings"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// HistoryConfig holds configuration for token-aware history retrieval.
// This is copied from config to avoid circular imports in some contexts.
type HistoryConfig = config.HistoryConfig

// buildChatMessages constructs the message history for AI context (legacy method)
func (r *Router) buildChatMessages(session *sessions.Session, userMessage string) ([]ChatMessage, error) {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "You are a helpful AI assistant. Be concise and direct in your responses.",
		},
	}

	// Add recent message history with token-aware retrieval
	recentMessages, err := r.getRecentMessagesTokenAware(session)
	if err != nil {
		return nil, err
	}
	// conduit-z7hu: history already contains the just-stored current user
	// message (store-before-call); drop the trailing duplicate so the current
	// message is not appended a second time below.
	recentMessages = dropTrailingCurrentUserDup(recentMessages, userMessage)

	for _, msg := range recentMessages {
		// Skip messages with empty content - Anthropic API requires non-empty content
		if msg.Content == "" {
			continue
		}
		messages = append(messages, ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Add current user message
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: userMessage,
	})

	return messages, nil
}

// dropTrailingCurrentUserDup removes the last history message when it is an
// exact duplicate of the current user message. conduit-z7hu: the gateway
// stores the incoming user message BEFORE calling the AI, then passes the
// same text as userMessage — appending it after verbatim history duplicates
// it in the prompt. Only the very last message is considered, and only on an
// exact role+content match, so identical texts from earlier turns (stored
// before this turn) are preserved. Exact match on empty/whitespace means
// nothing is dropped for a genuinely empty userMessage beyond what the
// empty-content skip already handles.
func dropTrailingCurrentUserDup(history []sessions.Message, userMessage string) []sessions.Message {
	if len(history) == 0 {
		return history
	}
	last := history[len(history)-1]
	if last.Role == "user" && last.Content == userMessage {
		return history[:len(history)-1]
	}
	return history
}

type currentUserMessageIDKey struct{}

// WithCurrentUserMessageID records the transcript ID of the user row that
// this turn's userMessage stands for (conduit-31jg.22). The gateway
// TurnRunner stores that row inside the turn lock, but the text sent to the
// model can differ from the stored text (photo markers, "[System: …]"
// reflection suffixes), which defeats the text-based dropTrailingCurrentUserDup.
// With the ID the history builder drops exactly that row instead.
func WithCurrentUserMessageID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, currentUserMessageIDKey{}, id)
}

func currentUserMessageIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(currentUserMessageIDKey{}).(string)
	return id
}

// dropCurrentUserRow removes the current turn's stored user row from history
// so it is not sent twice (it is appended below as the current message). By
// ID when the caller supplied one (conduit-31jg.22; the row may not be last,
// e.g. an inter-session wake message followed by later rows), else by the
// legacy trailing exact-text match (conduit-z7hu).
func dropCurrentUserRow(ctx context.Context, history []sessions.Message, userMessage string) []sessions.Message {
	if id := currentUserMessageIDFrom(ctx); id != "" {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].ID == id {
				out := make([]sessions.Message, 0, len(history)-1)
				out = append(out, history[:i]...)
				return append(out, history[i+1:]...)
			}
		}
		return history
	}
	return dropTrailingCurrentUserDup(history, userMessage)
}

// buildChatMessagesWithSystemPrompt constructs messages with agent system prompt.
// Attachments from the context (via WithAttachments) are set on the final user message.
func (r *Router) buildChatMessagesWithSystemPrompt(ctx context.Context, session *sessions.Session, userMessage string, systemBlocks []SystemBlock) ([]ChatMessage, error) {
	var messages []ChatMessage

	// Build system message from system blocks
	if len(systemBlocks) > 0 {
		var systemContent strings.Builder
		for i, block := range systemBlocks {
			if i > 0 {
				systemContent.WriteString("\n\n")
			}
			systemContent.WriteString(block.Text)
		}

		messages = append(messages, ChatMessage{
			Role:    "system",
			Content: systemContent.String(),
			// conduit-31jg.14: keep the block split for cache placement.
			SystemBlocks: append([]SystemBlock(nil), systemBlocks...),
		})
	}

	// Add recent message history with token-aware retrieval
	recentMessages, err := r.getRecentMessagesTokenAware(session)
	if err != nil {
		return nil, err
	}
	// conduit-z7hu: history already contains the just-stored current user
	// message (store-before-call); drop the trailing duplicate so the current
	// message is not appended a second time below. conduit-31jg.22: by
	// transcript ID when the TurnRunner supplied one.
	recentMessages = dropCurrentUserRow(ctx, recentMessages, userMessage)

	for _, msg := range recentMessages {
		// Skip messages with empty content - Anthropic API requires non-empty content
		if msg.Content == "" {
			continue
		}
		messages = append(messages, ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Add current user message with any attachments from context
	userMsg := ChatMessage{
		Role:    "user",
		Content: userMessage,
	}
	if attachments := AttachmentsFromContext(ctx); len(attachments) > 0 {
		userMsg.Attachments = attachments
	}
	messages = append(messages, userMsg)

	return messages, nil
}

// getRecentMessages retrieves recent messages from a session (legacy, fixed count)
func (r *Router) getRecentMessages(session *sessions.Session, limit int) ([]sessions.Message, error) {
	if r.sessionStore == nil {
		// No store available, return empty history
		fmt.Printf("[Router] WARNING: No session store available for history\n")
		return []sessions.Message{}, nil
	}

	// Retrieve messages from session store
	messages, err := r.sessionStore.GetMessages(session.Key, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}

	fmt.Printf("[Router] Retrieved %d messages from session %s\n", len(messages), session.Key)

	return messages, nil
}

// getRecentMessagesTokenAware retrieves messages using token budget instead of fixed count.
// This ensures long conversations retain meaningful context rather than arbitrary message counts.
func (r *Router) getRecentMessagesTokenAware(session *sessions.Session) ([]sessions.Message, error) {
	if r.sessionStore == nil {
		fmt.Printf("[Router] WARNING: No session store available for history\n")
		return []sessions.Message{}, nil
	}

	// Get config or use defaults
	cfg := r.getHistoryConfig()

	// Fetch more messages than we'll likely use, then trim by token budget
	messages, err := r.sessionStore.GetMessages(session.Key, cfg.MaxMessages)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}

	if len(messages) == 0 {
		return messages, nil
	}

	charsPerToken := cfg.CharsPerToken
	if charsPerToken <= 0 {
		charsPerToken = 4
	}
	charBudget := cfg.MaxTokens * charsPerToken

	// conduit-31jg.63: keep the previous cut while it fits; when over budget
	// re-cut down to the low-water mark (whole user turns) so the history
	// prefix — and its cache breakpoints — stays stable for many turns.
	fetchLimit := cfg.MaxMessages
	if fetchLimit <= 0 {
		fetchLimit = sessions.DefaultMessageLimit
	}
	prevCut, _ := r.historyCuts.get(session.Key)
	start := selectHistoryWindow(messages, prevCut, charBudget, fetchLimit, cfg.MinMessages,
		historyLowWater(cfg.LowWaterFraction), len(messages) < fetchLimit)
	selected := messages[start:]
	if id := selected[0].ID; id != "" {
		r.historyCuts.set(session.Key, id)
	}
	if id := selected[0].ID; start > 0 && id != prevCut {
		log.Printf("[Router] History trim: session %s cut to %d of %d messages (low-water, conduit-31jg.63)",
			session.Key, len(selected), len(messages))
	}

	usedChars := 0
	for _, m := range selected {
		usedChars += historyMsgChars(m)
	}
	estimatedTokens := usedChars / charsPerToken
	fmt.Printf("[Router] Token-aware retrieval: %d messages (~%d tokens) from session %s\n",
		len(selected), estimatedTokens, session.Key)

	return selected, nil
}

// trimRequestToFitContext estimates total token usage for a GenerateRequest and
// drops the oldest conversation history (whole user turns, with hysteresis —
// conduit-31jg.63) until the request fits within the model's context window. The system prompt (first message) and the current
// user message (last message) are always preserved.
// If contextWindowOverride > 0, it takes precedence over model-based detection.
func trimRequestToFitContext(req *GenerateRequest, contextWindowOverride int) {
	if req == nil || len(req.Messages) < 2 {
		return
	}

	contextWindow := contextWindowOverride
	if contextWindow <= 0 {
		contextWindow = ContextWindowForModel(req.Model)
	}
	charsPerToken := 4 // same estimate used elsewhere

	// Reserve space for model output
	outputReserve := req.MaxTokens
	if outputReserve <= 0 {
		outputReserve = 4000
	}

	// Budget available for the request (prompt tokens)
	budget := contextWindow - outputReserve

	// Estimate tool definition tokens (~JSON overhead per tool)
	toolChars := 0
	for _, t := range req.Tools {
		toolChars += len(t.Name) + len(t.Description) + 200 // rough JSON overhead
	}
	budgetChars := budget*charsPerToken - toolChars

	if budgetChars <= 0 {
		// Context window is too small even without history — nothing we can do
		return
	}

	// Estimate total message chars
	totalChars := 0
	for _, m := range req.Messages {
		totalChars += len(m.Role) + len(m.Content) + 10
	}

	if totalChars <= budgetChars {
		return // fits fine
	}

	// Need to trim. Preserve first message (system prompt) and last message (user input).
	// Drop oldest history messages (indices 1..len-2) until it fits.
	systemMsg := req.Messages[0]
	userMsg := req.Messages[len(req.Messages)-1]
	history := req.Messages[1 : len(req.Messages)-1]

	// Chars for the preserved messages
	fixedChars := len(systemMsg.Role) + len(systemMsg.Content) + 10 +
		len(userMsg.Role) + len(userMsg.Content) + 10
	availableChars := budgetChars - fixedChars

	if availableChars <= 0 {
		// System prompt + user message alone exceed budget — keep them anyway
		req.Messages = []ChatMessage{systemMsg, userMsg}
		log.Printf("[Router] Context trim: dropped all %d history messages (system+user alone ~%d tokens, budget %d)",
			len(history), fixedChars/charsPerToken, budget)
		return
	}

	// conduit-31jg.63: drop whole user turns, oldest first, snapped to
	// hysteresis chunks from the start of history, so the next turns (same
	// prefix, longer tail) keep the same first message instead of dropping
	// one more message each turn.
	groups := turnGroups(history, 0, len(history))
	groupChars := make([]int, len(groups))
	for gi, g := range groups {
		for _, m := range history[g[0]:g[1]] {
			groupChars[gi] += len(m.Role) + len(m.Content) + 10
		}
	}
	dropGroups := snappedDropCount(groupChars, availableChars, hysteresisChunk(budgetChars))
	keepFrom := len(history)
	if dropGroups < len(groups) {
		keepFrom = groups[dropGroups][0]
	}
	keptChars := 0
	for _, c := range groupChars[dropGroups:] {
		keptChars += c
	}

	dropped := keepFrom
	if dropped > 0 {
		trimmed := make([]ChatMessage, 0, 1+len(history)-dropped+1)
		trimmed = append(trimmed, systemMsg)
		trimmed = append(trimmed, history[keepFrom:]...)
		trimmed = append(trimmed, userMsg)
		req.Messages = trimmed

		estimatedTokens := (fixedChars + keptChars + toolChars) / charsPerToken
		log.Printf("[Router] Context trim: dropped %d oldest history messages to fit %s context (%d tokens, ~%d token estimate, budget %d)",
			dropped, req.Model, contextWindow, estimatedTokens, budget)
	}
}

// getHistoryConfig returns the history configuration, with defaults if not set
func (r *Router) getHistoryConfig() HistoryConfig {
	if r.historyConfig != nil {
		return *r.historyConfig
	}
	return config.DefaultHistoryConfig()
}
