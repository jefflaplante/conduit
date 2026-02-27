package ai

import (
	"fmt"
	"strings"

	"conduit/internal/sessions"
)

// buildChatMessages constructs the message history for AI context (legacy method)
func (r *Router) buildChatMessages(session *sessions.Session, userMessage string) ([]ChatMessage, error) {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "You are a helpful AI assistant. Be concise and direct in your responses.",
		},
	}

	// Add recent message history (limit to last 20 messages for context)
	recentMessages, err := r.getRecentMessages(session, 20)
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

	// Add recent message history (limit to last 20 messages for context)
	recentMessages, err := r.getRecentMessages(session, 20)
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

// getRecentMessages retrieves recent messages from a session
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
