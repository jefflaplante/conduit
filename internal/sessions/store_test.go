package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStore(t *testing.T) {
	// Create temporary database file
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Verify the database file was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestGetOrCreateSession(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	userID := "user123"
	channelID := "channel456"

	// Create a session
	session, err := store.GetOrCreateSession(userID, channelID)
	if err != nil {
		t.Fatalf("Failed to get or create session: %v", err)
	}

	if session.Key == "" {
		t.Error("Expected session key to be set")
	}

	if session.UserID != userID {
		t.Errorf("Expected user ID %s, got %s", userID, session.UserID)
	}

	if session.ChannelID != channelID {
		t.Errorf("Expected channel ID %s, got %s", channelID, session.ChannelID)
	}

	// Retrieve the session
	retrievedSession, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if retrievedSession.Key != session.Key {
		t.Errorf("Expected session key %s, got %s", session.Key, retrievedSession.Key)
	}
}

func TestAddAndRetrieveMessage(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a session
	session, err := store.GetOrCreateSession("user123", "channel456")
	if err != nil {
		t.Fatalf("Failed to get or create session: %v", err)
	}

	// Add a message
	metadata := make(map[string]string)
	metadata["source"] = "test"

	message, err := store.AddMessage(session.Key, "user", "Hello, world!", metadata)
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	if message.ID == "" {
		t.Error("Expected message ID to be set")
	}

	if message.Role != "user" {
		t.Errorf("Expected message role 'user', got %s", message.Role)
	}

	if message.Content != "Hello, world!" {
		t.Errorf("Expected message content 'Hello, world!', got %s", message.Content)
	}

	// Retrieve messages
	messages, err := store.GetMessages(session.Key, 0) // 0 = no limit
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Role != "user" {
		t.Errorf("Expected message role 'user', got %s", messages[0].Role)
	}

	if messages[0].Content != "Hello, world!" {
		t.Errorf("Expected message content 'Hello, world!', got %s", messages[0].Content)
	}
}

func TestGetNonExistentSession(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	_, err = store.GetSession("non-existent-session")
	if err == nil {
		t.Error("Expected error when getting non-existent session")
	}
}

func TestGetLatestSession(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	userID := "user123"
	channelID := "channel456"

	// Create first session
	session1, err := store.GetOrCreateSession(userID, channelID)
	if err != nil {
		t.Fatalf("Failed to create first session: %v", err)
	}

	// Add a message to create some activity
	_, err = store.AddMessage(session1.Key, "user", "First message", nil)
	if err != nil {
		t.Fatalf("Failed to add message to first session: %v", err)
	}

	time.Sleep(2 * time.Millisecond) // Ensure different timestamps

	// Create second session (this will be a new session since GetOrCreate
	// may return the same session. We need to manually create one)
	userID2 := "user456" // Use different user to force new session
	session2, err := store.GetOrCreateSession(userID2, channelID)
	if err != nil {
		t.Fatalf("Failed to create second session: %v", err)
	}

	// Add a message to the second session
	_, err = store.AddMessage(session2.Key, "user", "Second message", nil)
	if err != nil {
		t.Fatalf("Failed to add message to second session: %v", err)
	}

	// Get latest session for second user
	latestSession, err := store.GetLatestSession(userID2, channelID)
	if err != nil {
		t.Fatalf("Failed to get latest session: %v", err)
	}

	// Should return the session for user2
	if latestSession.Key != session2.Key {
		t.Errorf("Expected latest session key %s, got %s", session2.Key, latestSession.Key)
	}
}

func TestAddMultipleMessages(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	session, err := store.GetOrCreateSession("user123", "channel456")
	if err != nil {
		t.Fatalf("Failed to get or create session: %v", err)
	}

	testMessages := []struct {
		role    string
		content string
	}{
		{"user", "First message"},
		{"assistant", "Second message"},
		{"user", "Third message"},
	}

	// Add all messages
	for _, msg := range testMessages {
		_, err = store.AddMessage(session.Key, msg.role, msg.content, nil)
		if err != nil {
			t.Fatalf("Failed to add message '%s': %v", msg.content, err)
		}
	}

	// Retrieve messages
	retrievedMessages, err := store.GetMessages(session.Key, 0) // 0 = no limit
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(retrievedMessages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(retrievedMessages))
	}

	// Verify message content and order (messages should be in timestamp order)
	for i, expectedMsg := range testMessages {
		if retrievedMessages[i].Role != expectedMsg.role {
			t.Errorf("Message %d: expected role %s, got %s", i, expectedMsg.role, retrievedMessages[i].Role)
		}
		if retrievedMessages[i].Content != expectedMsg.content {
			t.Errorf("Message %d: expected content %s, got %s", i, expectedMsg.content, retrievedMessages[i].Content)
		}
	}

	// Test session message count was updated
	updatedSession, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("Failed to get updated session: %v", err)
	}

	if updatedSession.MessageCount != 3 {
		t.Errorf("Expected session message count 3, got %d", updatedSession.MessageCount)
	}
}

func TestGetSessionByLabel(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a session and set a label
	session, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	err = store.SetSessionContext(session.Key, "label", "my-agent")
	if err != nil {
		t.Fatalf("Failed to set label: %v", err)
	}

	// Lookup by label
	found, err := store.GetSessionByLabel("my-agent")
	if err != nil {
		t.Fatalf("Failed to get session by label: %v", err)
	}

	if found.Key != session.Key {
		t.Errorf("Expected session key %s, got %s", session.Key, found.Key)
	}

	// Lookup non-existent label
	_, err = store.GetSessionByLabel("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent label")
	}

	// Empty label
	_, err = store.GetSessionByLabel("")
	if err == nil {
		t.Error("Expected error for empty label")
	}
}
