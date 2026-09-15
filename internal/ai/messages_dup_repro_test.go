package ai

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// TestDupUserMsg_CurrentStoredThenAppendedAgain reproduces the doubled user
// message: gateway stores the incoming message (gateway.go:1028) BEFORE the AI
// call, then passes msg.Text as userMessage; buildChatMessagesWithSystemPrompt
// loads history (already containing the stored current message) and appends
// userMessage again.
func TestDupUserMsg_CurrentStoredThenAppendedAgain(t *testing.T) {
	store, err := sessions.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	router, err := NewRouter(config.AIConfig{}, nil)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.SetSessionStore(store)

	// Session as the gateway would have it: prior history + the current user
	// message already stored (gateway.go:1028) before the AI request.
	session, err := store.GetOrCreateSession("8594266914", "telegram")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}
	if _, err := store.AddMessage(session.Key, "assistant", "prior assistant reply", nil); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	currentText := "check into why you see doubled identical messages"
	if _, err := store.AddMessage(session.Key, "user", currentText, nil); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Gateway calls with msg.Text (same text that was just stored).
	messages, err := router.buildChatMessagesWithSystemPrompt(context.Background(), session, currentText, nil)
	if err != nil {
		t.Fatalf("buildChatMessagesWithSystemPrompt: %v", err)
	}

	userCopies := 0
	for _, m := range messages {
		if m.Role == "user" && strings.Contains(m.Content, "doubled identical messages") {
			userCopies++
		}
	}
	t.Logf("user copies of current message in prompt: %d (total messages: %d)", userCopies, len(messages))
	if userCopies != 1 {
		t.Errorf("expected exactly 1 copy of the current user message, got %d", userCopies)
	}
}
