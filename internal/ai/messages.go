package ai

import (
	"fmt"
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

// buildChatMessagesWithSystemPrompt constructs messages with agent system prompt
func (r *Router) buildChatMessagesWithSystemPrompt(session *sessions.Session, userMessage string, systemBlocks []SystemBlock) ([]ChatMessage, error) {
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
		})
	}

	// Add recent message history with token-aware retrieval
	recentMessages, err := r.getRecentMessagesTokenAware(session)
	if err != nil {
		return nil, err
	}

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

	// Calculate token budget and select messages newest-first
	tokenBudget := cfg.MaxTokens
	charsPerToken := cfg.CharsPerToken
	if charsPerToken <= 0 {
		charsPerToken = 4
	}

	charBudget := tokenBudget * charsPerToken
	usedChars := 0

	// Messages are returned chronologically (oldest first), so iterate from end
	var selected []sessions.Message
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		msgChars := len(msg.Content) + len(msg.Role) + 10 // overhead for role/structure

		// Always include minimum messages
		if len(selected) < cfg.MinMessages {
			selected = append(selected, msg)
			usedChars += msgChars
			continue
		}

		// Check if we have budget for more
		if usedChars+msgChars <= charBudget {
			selected = append(selected, msg)
			usedChars += msgChars
		} else {
			// Budget exhausted
			break
		}
	}

	// Reverse to restore chronological order
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}

	estimatedTokens := usedChars / charsPerToken
	fmt.Printf("[Router] Token-aware retrieval: %d messages (~%d tokens) from session %s\n",
		len(selected), estimatedTokens, session.Key)

	return selected, nil
}

// getHistoryConfig returns the history configuration, with defaults if not set
func (r *Router) getHistoryConfig() HistoryConfig {
	if r.historyConfig != nil {
		return *r.historyConfig
	}
	return config.DefaultHistoryConfig()
}
