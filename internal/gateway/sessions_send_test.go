package gateway

import (
	"context"
	"path/filepath"
	"testing"

	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

func TestSendToSession_ByKey(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a target session
	target, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("Failed to create target session: %v", err)
	}

	// Create a minimal gateway with just the session store
	gw := &Gateway{
		sessions: store,
	}

	// Send a message by key
	ctx := types.WithRequestContext(context.Background(), "ch-sender", "sender-user", "sender-session")
	err = gw.SendToSession(ctx, target.Key, "", "Hello from another session")
	if err != nil {
		t.Fatalf("SendToSession failed: %v", err)
	}

	// Verify message was added
	messages, err := store.GetMessages(target.Key, 0)
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	msg := messages[0]
	if msg.Role != "user" {
		t.Errorf("Expected role 'user', got %q", msg.Role)
	}
	if msg.Content != "Hello from another session" {
		t.Errorf("Expected content 'Hello from another session', got %q", msg.Content)
	}
	if msg.Metadata["source"] != "inter_session" {
		t.Errorf("Expected metadata source 'inter_session', got %q", msg.Metadata["source"])
	}
	if msg.Metadata["sender_session"] != "sender-session" {
		t.Errorf("Expected sender_session 'sender-session', got %q", msg.Metadata["sender_session"])
	}
}

func TestSendToSession_ByLabel(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create and label a session
	target, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("Failed to create target session: %v", err)
	}
	err = store.SetSessionContext(target.Key, "label", "research-agent")
	if err != nil {
		t.Fatalf("Failed to set label: %v", err)
	}

	gw := &Gateway{
		sessions: store,
	}

	// Send by label
	ctx := context.Background()
	err = gw.SendToSession(ctx, "", "research-agent", "Task update")
	if err != nil {
		t.Fatalf("SendToSession by label failed: %v", err)
	}

	// Verify
	messages, err := store.GetMessages(target.Key, 0)
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}
	if messages[0].Content != "Task update" {
		t.Errorf("Unexpected message content: %q", messages[0].Content)
	}
}

func TestSendToSession_Errors(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	gw := &Gateway{
		sessions: store,
	}

	ctx := context.Background()

	// No key or label
	err = gw.SendToSession(ctx, "", "", "test")
	if err == nil {
		t.Error("Expected error when neither key nor label provided")
	}

	// Non-existent key
	err = gw.SendToSession(ctx, "nonexistent-key", "", "test")
	if err == nil {
		t.Error("Expected error for non-existent session key")
	}

	// Non-existent label
	err = gw.SendToSession(ctx, "", "nonexistent-label", "test")
	if err == nil {
		t.Error("Expected error for non-existent label")
	}
}
