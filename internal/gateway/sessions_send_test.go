package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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
	// H4: a sender of the same user on the same channel may deliver.
	ctx := types.WithRequestContext(context.Background(), "ch1", "user1", "sender-session")
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

	// Send by label (as the owner: H4 authorizes the resolved target)
	ctx := ownerTestCtx()
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

func TestSendToSessionWake_MessageDelivered(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	target, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("Failed to create target session: %v", err)
	}

	// Use a buffered sessionWake channel so SendToSessionWake doesn't block
	gw := &Gateway{
		sessions:    store,
		sessionWake: make(chan string, 8),
	}

	ctx := types.WithRequestContext(context.Background(), "ch1", "user1", "sender-key")
	err = gw.SendToSessionWake(ctx, target.Key, "", "Wake up!")
	if err != nil {
		t.Fatalf("SendToSessionWake failed: %v", err)
	}

	// Message should be stored in the target session
	messages, err := store.GetMessages(target.Key, 0)
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}
	if messages[0].Content != "Wake up!" {
		t.Errorf("Unexpected message content: %q", messages[0].Content)
	}
	if messages[0].Metadata["source"] != "inter_session" {
		t.Errorf("Expected metadata source 'inter_session', got %q", messages[0].Metadata["source"])
	}
	if messages[0].Metadata["sender_session"] != "sender-key" {
		t.Errorf("Expected sender_session 'sender-key', got %q", messages[0].Metadata["sender_session"])
	}

	// Wake signal should have been sent to the channel
	select {
	case key := <-gw.sessionWake:
		if key != target.Key {
			t.Errorf("Expected wake signal for session %q, got %q", target.Key, key)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected wake signal in sessionWake channel, got none")
	}
}

func TestSendToSessionWake_ByLabel(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	target, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("Failed to create target session: %v", err)
	}
	if err := store.SetSessionContext(target.Key, "label", "worker-agent"); err != nil {
		t.Fatalf("Failed to set label: %v", err)
	}

	gw := &Gateway{
		sessions:    store,
		sessionWake: make(chan string, 8),
	}

	err = gw.SendToSessionWake(ownerTestCtx(), "", "worker-agent", "Task result")
	if err != nil {
		t.Fatalf("SendToSessionWake by label failed: %v", err)
	}

	messages, err := store.GetMessages(target.Key, 0)
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "Task result" {
		t.Errorf("Expected 'Task result', got messages: %+v", messages)
	}

	// Wake signal sent
	select {
	case <-gw.sessionWake:
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected wake signal, got none")
	}
}

func TestSendToSessionWake_Errors(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	gw := &Gateway{
		sessions:    store,
		sessionWake: make(chan string, 8),
	}

	ctx := context.Background()

	// No key or label
	err = gw.SendToSessionWake(ctx, "", "", "test")
	if err == nil {
		t.Error("Expected error when neither key nor label provided")
	}

	// Non-existent session
	err = gw.SendToSessionWake(ctx, "no-such-key", "", "test")
	if err == nil {
		t.Error("Expected error for non-existent session key")
	}
}

func TestWakeSession_RecursionGuard(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	target, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("Failed to create target session: %v", err)
	}
	// A wake message beyond the max depth (H4: depth travels in metadata;
	// TestWakeSession_RefusesMessageBeyondMaxDepth covers it with a model).
	if _, err := store.AddMessage(target.Key, "user", "hello", map[string]string{
		"source": "inter_session", metaWakeDepth: "4",
	}); err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	gw := &Gateway{
		sessions:    store,
		sessionWake: make(chan string, 8),
		// ai is nil — if wakeSession tries to call the AI, it will panic or fail.
		// The recursion guard should prevent it from getting that far.
	}

	// Should return immediately without calling AI (ai == nil would panic if reached)
	gw.wakeSession(target.Key)

	// The message stays queued for the next activation.
	if n := messageCount(t, store, target.Key); n != 1 {
		t.Errorf("Expected the queued message to remain, got %d message(s)", n)
	}
}

func TestWakeSession_NoAI(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	gw := &Gateway{
		sessions:    store,
		sessionWake: make(chan string, 8),
		// ai is nil — wakeSession should return early without panicking
	}

	// Non-existent session key — should not panic
	gw.wakeSession("no-such-session")

	// Should complete without error
}

func TestSendToSessionWake_WakeChannelFull(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	target, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("Failed to create target session: %v", err)
	}

	// Create a FULL wake channel (capacity 0 acts as synchronous, use capacity 1 already full)
	wakeCh := make(chan string, 1)
	wakeCh <- "some-other-session" // fill it up

	gw := &Gateway{
		sessions:    store,
		sessionWake: wakeCh,
	}

	// Should succeed (message delivered) even though wake signal is dropped
	err = gw.SendToSessionWake(ownerTestCtx(), target.Key, "", "queued message")
	if err != nil {
		t.Fatalf("SendToSessionWake should succeed even with full wake channel: %v", err)
	}

	// Message should still be stored
	messages, err := store.GetMessages(target.Key, 0)
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "queued message" {
		t.Errorf("Expected message to be stored, got: %+v", messages)
	}
}
